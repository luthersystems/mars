package infrarepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shell cases A and B: the custom-stack apply (apply-with-outputs.sh
// --check-drift, apply-plan.sh) persists the stack to an empty -infra repo.
func TestPersistPopulatesEmptyInfraRepo(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)

	r, out := newRepo(proj)
	r.Persist(ctx())

	assertContains(t, out.String(), "[git-infra] Persisting applied stack")
	assertContains(t, out.String(), "[git-infra] Pushed to infra remote")
	if commitCount(t, bare) < 1 {
		t.Fatalf("the -infra repo is still empty after a persist (#143):\n%s", out)
	}
	if git(t, bare, "for-each-ref", "--format=%(refname)") != "refs/heads/main" {
		t.Errorf("no refs/heads/main on the infra repo")
	}
}

// Shell case C: no .git and a remote nobody can clone: a loud warning,
// nothing pushed.
func TestPersistWithoutGitWarns(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(filepath.Join(t.TempDir(), "no-such-repo.git")))

	r, out := newRepo(proj)
	r.Persist(ctx())

	assertContains(t, out.String(), "[git-infra] WARNING:")
	assertContains(t, out.String(), "no .git")
	if fileExists(filepath.Join(proj, ".git")) {
		t.Errorf("a failed clone left a .git behind")
	}
	if commitCount(t, bare) != 0 {
		t.Errorf("something was pushed without a .git")
	}
}

// Shell case D: no repo_clone_ssh_url: loud warnings, no infra remote.
func TestPersistWithoutURLWarns(t *testing.T) {
	proj := newProject(t)
	initGit(t, proj)

	r, out := newRepo(proj)
	r.Persist(ctx())

	assertContains(t, out.String(), "repo_clone_ssh_url is empty")
	assertContains(t, out.String(), "no 'infra' remote configured")
}

// Shell case F: a second deploy adds a commit (fast-forward).
func TestPersistRedeployAdvances(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)

	r, _ := newRepo(proj)
	r.Persist(ctx())
	first := commitCount(t, bare)
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/main.tf"), "second-deploy-change\n", 0o644)
	r.Persist(ctx())
	if second := commitCount(t, bare); first < 1 || second <= first {
		t.Errorf("re-deploy did not advance the -infra repo (%d → %d)", first, second)
	}
}

// Shell case G: the #160 canary: no tfvars, outputs, secrets, plan or
// backend file reaches the -infra repo; known-good files do.
func TestPersistKeepsSecretsOut(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, "secrets/cloud-credentials.auto.tfvars.json"), `{"aws_external_id":"topsecret"}`, 0o644)
	writeFile(t, filepath.Join(proj, "secrets/infra_deploy_key.pem"), "deploy-key-material\n", 0o644)
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/common.auto.tfvars.json"), `{"cloud_provider":"aws"}`, 0o644)
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/zz-secret.auto.tfvars.json"), `{"k":"v"}`, 0o644)
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/apply.tfplan"), "plan-bytes\n", 0o644)
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/backend.tf.json"), "{}\n", 0o644)

	r, out := newRepo(proj)
	r.Persist(ctx())

	pushed := tree(t, bare, "main")
	if !hasPath(pushed, "shell_utils.sh") {
		t.Fatalf("nothing pushed; the canary would be vacuous:\n%s", out)
	}
	for _, p := range pushed {
		for _, bad := range []string{".auto.tfvars.json", ".tfplan", "backend.tf.json"} {
			if strings.HasSuffix(p, bad) {
				t.Errorf("%s rode the -infra push (#160)", p)
			}
		}
		if strings.HasPrefix(p, "outputs/") || strings.HasPrefix(p, "secrets/") {
			t.Errorf("%s rode the -infra push (#160)", p)
		}
	}
}

// Shell case H: the composer's provider siblings are pushed; the
// deploy-generated providers-<cloud>.tf is not.
func TestPersistProviderFiles(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	dir := filepath.Join(proj, "tf/custom-stack-provision")
	writeFile(t, filepath.Join(dir, "providers-aws.tf"), "# generated\n", 0o644)
	writeFile(t, filepath.Join(dir, "providers-imported.tf"), `provider "aws" { alias = "imported" }`+"\n", 0o644)
	writeFile(t, filepath.Join(dir, "providers-aliases.tf"), `provider "aws" { alias = "us_east_1" }`+"\n", 0o644)

	r, out := newRepo(proj)
	r.Persist(ctx())

	pushed := tree(t, bare, "main")
	if len(pushed) == 0 {
		t.Fatalf("nothing pushed:\n%s", out)
	}
	for _, p := range []string{"providers-imported.tf", "providers-aliases.tf"} {
		if !hasPath(pushed, "tf/custom-stack-provision/"+p) {
			t.Errorf("%s missing from the snapshot", p)
		}
	}
	if hasPath(pushed, "tf/custom-stack-provision/providers-aws.tf") {
		t.Errorf("the generated providers-aws.tf leaked into the snapshot")
	}
}

// Shell case I: a tree without .git (the GitHub App flow: prepare had no
// token) is cloned by the persist step, pushed on top of infra/main, the
// mark is cleared, and a later pull merges a teammate's push.
func TestPersistClonesAndClearsMark(t *testing.T) {
	bare := newBare(t)
	src := filepath.Join(t.TempDir(), "seed")
	git(t, ".", "init", "-q", "-b", "main", src)
	git(t, src, "commit", "-q", "--allow-empty", "-m", "earlier deploy")
	git(t, src, "push", "-q", fileURL(bare), "main")
	proj := newProject(t)
	setRepoURL(t, proj, fileURL(bare))
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/main.tf"), `resource "null_resource" "x" {}`+"\n", 0o644)

	r, out := newRepo(proj)
	r.Persist(ctx())

	if commitCount(t, bare) < 2 {
		t.Fatalf("expected a new commit on the -infra repo:\n%s", out)
	}
	if !hasPath(tree(t, bare, "main"), "tf/custom-stack-provision/main.tf") {
		t.Errorf("custom stack missing from the pushed tree")
	}
	if v := configValue(proj, snapshotTreeKey); v != "" {
		t.Errorf("the snapshot did not clear %s (%q)", snapshotTreeKey, v)
	}
	mateCommit(t, bare, "mate.txt", "mate\n")
	if rc := r.Pull(ctx()); rc != 0 || !fileExists(filepath.Join(proj, "mate.txt")) {
		t.Errorf("pull after the snapshot: rc=%d\n%s", rc, out)
	}
}

// Shell case K: a TRACKED credentials file is untracked, not pushed, and
// stays on disk.
func TestPersistUntracksTrackedCredentials(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, "tf/auto-vars/common.auto.tfvars.json"), "{}\n", 0o644)
	git(t, proj, "add", "-f", "tf/auto-vars/common.auto.tfvars.json")
	git(t, proj, "commit", "-q", "-m", "old template: seed tracked")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	setAutoVars(t, proj, map[string]any{"cloud_provider": "aws", repoURLVar: fileURL(bare), "aws_secret_access_key": "FAKE-SECRET-K"})

	r, out := newRepo(proj)
	r.Persist(ctx())

	pushed := tree(t, bare, "main")
	if !hasPath(pushed, "shell_utils.sh") {
		t.Fatalf("the stack was not pushed:\n%s", out)
	}
	if hasPath(pushed, "tf/auto-vars/common.auto.tfvars.json") {
		t.Errorf("the tracked common.auto.tfvars.json is still in the pushed tree (#160)")
	}
	if strings.Contains(git(t, bare, "log", "-p", "--all"), "FAKE-SECRET-K") {
		t.Errorf("the deploy-time secret reached the -infra repo history (#160)")
	}
	if !strings.Contains(readFile(t, filepath.Join(proj, "tf/auto-vars/common.auto.tfvars.json")), "FAKE-SECRET-K") {
		t.Errorf("the credentials file was removed from disk")
	}
	// The log names each untracked file: it is the record that a
	// credentials file was tracked, and so pushed, before.
	assertContains(t, out.String(), "Untracking file(s) that .gitignore ignores (#160):\n    tf/auto-vars/common.auto.tfvars.json\n")
}

// Shell case M: after ui-core squashes the project history, the next deploy
// still lands as a fast-forward snapshot of exactly the deployed tree.
func TestPersistAfterSquash(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	r, out := newRepo(proj)
	r.Persist(ctx())
	if head(bare) == "" {
		t.Fatalf("the first persist pushed nothing:\n%s", out)
	}

	// infra/main gets a file the deploy does not have, an edit to a file
	// the deploy has, and an executable script the deploy holds as 0644.
	stale := filepath.Join(t.TempDir(), "stale")
	git(t, ".", "clone", "-q", "-b", "main", fileURL(bare), stale)
	writeFile(t, filepath.Join(stale, "stale-infra-only.txt"), "infra-only file\n", 0o644)
	writeFile(t, filepath.Join(stale, "shell_utils.sh"), readFile(t, filepath.Join(stale, "shell_utils.sh"))+"# infra-only edit\n", 0o644)
	writeFile(t, filepath.Join(stale, "existing-hook.sh"), "#!/bin/sh\n", 0o755)
	git(t, stale, "add", "stale-infra-only.txt", "shell_utils.sh", "existing-hook.sh")
	git(t, stale, "commit", "-q", "-m", "infra-only edit")
	git(t, stale, "push", "-q", "origin", "main")
	staleHead := git(t, bare, "rev-parse", "refs/heads/main")
	// A tag named main on another commit: fetching "main" would take it.
	git(t, stale, "commit", "-q", "--allow-empty", "-m", "tagged, not on main")
	git(t, stale, "tag", "main", "HEAD")
	git(t, stale, "push", "-q", "origin", "refs/tags/main")

	// The ui-core squash: a new root commit that tracks the credentials
	// file, as projects built from the old template do.
	if err := os.RemoveAll(filepath.Join(proj, ".git")); err != nil {
		t.Fatal(err)
	}
	initGit(t, proj)
	git(t, proj, "add", "-A")
	git(t, proj, "add", "-f", "tf/auto-vars/common.auto.tfvars.json")
	git(t, proj, "commit", "-q", "-m", "ui-core: squashed project history")
	setAutoVars(t, proj, map[string]any{"cloud_provider": "aws", repoURLVar: fileURL(bare), "aws_secret_access_key": "FAKE-SECRET-M"})
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/main.tf"), "third-deploy-change\n", 0o644)
	writeFile(t, filepath.Join(proj, "existing-hook.sh"), "#!/bin/sh\n", 0o644)
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/new-hook.sh"), "#!/bin/sh\necho hi\n", 0o755)
	git(t, proj, "fetch", "-q", fileURL(bare), "refs/heads/main")
	if _, err := gitErr(proj, "merge-base", "HEAD", "FETCH_HEAD"); err == nil {
		t.Fatal("fixture: the squashed history shares a commit with infra/main")
	}

	r.Persist(ctx())

	newHead := git(t, bare, "rev-parse", "refs/heads/main")
	if newHead == staleHead {
		t.Fatalf("infra/main did not move:\n%s", out)
	}
	if _, err := gitErr(bare, "merge-base", "--is-ancestor", staleHead, newHead); err != nil {
		t.Errorf("the push was not a fast-forward of infra/main")
	}
	if got := git(t, bare, "show", "refs/heads/main:tf/custom-stack-provision/main.tf"); got != "third-deploy-change" {
		t.Errorf("pushed main.tf = %q, want the deployed one", got)
	}
	if hasPath(tree(t, bare, "refs/heads/main"), "stale-infra-only.txt") {
		t.Errorf("a file only on infra/main survived the snapshot")
	}
	if strings.Contains(git(t, bare, "show", "refs/heads/main:shell_utils.sh"), "# infra-only edit") {
		t.Errorf("a file in both places kept infra/main's version")
	}
	if m := mode(t, bare, "refs/heads/main", "tf/custom-stack-provision/new-hook.sh"); m != "100755" {
		t.Errorf("a new 0755 script is %s, want 100755", m)
	}
	if m := mode(t, bare, "refs/heads/main", "existing-hook.sh"); m != "100755" {
		t.Errorf("an existing 100755 file that is 0644 on disk is %s", m)
	}
	if n := git(t, bare, "rev-list", "--count", staleHead+"..refs/heads/main"); n != "1" {
		t.Errorf("expected one snapshot commit on top of infra/main, got %s", n)
	}
	for _, p := range tree(t, bare, "refs/heads/main") {
		if strings.HasSuffix(p, ".auto.tfvars.json") || strings.HasPrefix(p, "outputs/") ||
			strings.HasPrefix(p, "secrets/") || strings.HasSuffix(p, ".tfplan") {
			t.Errorf("%s rode the snapshot push (#160)", p)
		}
	}
	if strings.Contains(git(t, bare, "log", "-p", "--all"), "FAKE-SECRET-M") {
		t.Errorf("the deploy-time secret reached the -infra repo history (#160)")
	}
	if !strings.Contains(readFile(t, filepath.Join(proj, "tf/auto-vars/common.auto.tfvars.json")), "FAKE-SECRET-M") {
		t.Errorf("the credentials file was removed from disk")
	}
}

// Shell case O: the snapshot drops template-only files (and removes one an
// older push added); the file stays on disk.
func TestSnapshotDropsTemplateOnlyFiles(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, ".github/dependabot.yml"), readTestdata(t, "dependabot.yml"), 0o644)
	writeFile(t, filepath.Join(proj, ".github/workflows/security.yml"), "name: security\n", 0o644)
	commitAll(t, proj, "older push, with dependabot.yml")
	git(t, proj, "push", "-q", fileURL(bare), "main")

	r, out := newRepo(proj)
	r.Persist(ctx())

	pushed := tree(t, bare, "main")
	if !hasPath(pushed, "shell_utils.sh") {
		t.Fatalf("nothing pushed:\n%s", out)
	}
	if hasPath(pushed, ".github/dependabot.yml") {
		t.Errorf(".github/dependabot.yml reached the -infra repo")
	}
	if !hasPath(pushed, ".github/workflows/security.yml") {
		t.Errorf("a workflow file was dropped; only template-only paths go")
	}
	if !fileExists(filepath.Join(proj, ".github/dependabot.yml")) {
		t.Errorf("dependabot.yml was removed from disk")
	}
}
