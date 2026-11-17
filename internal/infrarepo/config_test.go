package infrarepo

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sshURL = "git@github.com:luthersystems-infra-test/abcd1234-infra.git"

// Shell case J: with the GitHub App token, git uses HTTPS and gets the
// token from a credential helper; the token never lands in .git/config, and
// an earlier deploy key's sshCommand is removed.
func TestConfigureWithToken(t *testing.T) {
	proj := newProject(t)
	tokenFile := filepath.Join(t.TempDir(), "app-token")
	writeFile(t, tokenFile, "ghs_testtoken\n", 0o600)
	setRepoURL(t, proj, sshURL)
	initGit(t, proj)
	git(t, proj, "config", "core.sshCommand", "ssh -i old-key")

	r, out := newRepo(proj, withEnv(map[string]string{"GITHUB_GIT_TOKEN_FILE": tokenFile}))
	if !r.configure(ctx()) {
		t.Fatalf("configure failed:\n%s", out)
	}

	if got := git(t, proj, "remote", "get-url", "infra"); got != "https://github.com/luthersystems-infra-test/abcd1234-infra.git" {
		t.Errorf("infra remote = %q, want the HTTPS URL", got)
	}
	cmd := exec.Command("git", "credential", "fill")
	cmd.Dir = proj
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=luthersystems-infra-test/abcd1234-infra.git\n\n")
	fill, err := cmd.Output()
	if err != nil {
		t.Fatalf("git credential fill: %v", err)
	}
	if !strings.Contains(string(fill), "password=ghs_testtoken\n") || !strings.Contains(string(fill), "username=x-access-token\n") {
		t.Errorf("the credential helper did not give the token:\n%s", fill)
	}
	if v := configValue(proj, "core.sshCommand"); v != "" {
		t.Errorf("core.sshCommand still set: %q", v)
	}
	if strings.Contains(readFile(t, filepath.Join(proj, ".git/config")), "ghs_testtoken") {
		t.Errorf("the token is in .git/config, which travels in the project archive")
	}
	if v := configValue(proj, "core.fileMode"); v != "false" {
		t.Errorf("core.fileMode = %q, want false", v)
	}
	if v := configValue(proj, "user.email"); v != "test@luthersystems.com" {
		t.Errorf("configure replaced an existing identity: %q", v)
	}
}

// Shell case J: without a token the SSH URL stays, and a deploy key is used
// when there is one.
func TestConfigureWithoutToken(t *testing.T) {
	proj := newProject(t)
	setRepoURL(t, proj, sshURL)
	initGit(t, proj)
	git(t, proj, "config", "--unset", "user.email")

	r, out := newRepo(proj)
	if got := r.remoteURL(); got != sshURL {
		t.Errorf("remote URL without a token = %q, want the SSH URL", got)
	}
	r.configure(ctx())
	assertContains(t, out.String(), "Skipping SSH config: no deploy key")
	if v := configValue(proj, "user.email"); v != "devbot@luthersystems.com" {
		t.Errorf("identity not set: %q", v)
	}

	key := filepath.Join(proj, "secrets/infra_deploy_key.pem")
	writeFile(t, key, "key\n", 0o600)
	r.configure(ctx())
	if v := configValue(proj, "core.sshCommand"); v != "ssh -i "+key+" -o IdentitiesOnly=yes" {
		t.Errorf("core.sshCommand = %q", v)
	}
}

// Shell case J: with no token and no deploy key, an SSH clone is skipped
// (non-fatal).
func TestCloneSkipsSSHWithoutCredentials(t *testing.T) {
	proj := newProject(t)
	setRepoURL(t, proj, sshURL)
	r, out := newRepo(proj)
	r.Clone(ctx())
	assertContains(t, out.String(), "No GitHub token or deploy key")
	if fileExists(filepath.Join(proj, ".git")) {
		t.Errorf("a .git appeared")
	}
}

// getTfVar's order: tf/auto-vars/*.json first, then secrets/; empty and
// null values are skipped.
func TestTfVarOrder(t *testing.T) {
	proj := newProject(t)
	writeFile(t, filepath.Join(proj, "tf/auto-vars/a.json"), `{"k": null, "n": 3}`, 0o644)
	writeFile(t, filepath.Join(proj, "tf/auto-vars/b.json"), `{"k": ""}`, 0o644)
	writeFile(t, filepath.Join(proj, "tf/auto-vars/c.json"), `not json`, 0o644)
	writeFile(t, filepath.Join(proj, "secrets/x.auto.tfvars.json"), `{"k": "from-secrets"}`, 0o644)
	r, _ := newRepo(proj)
	if got := r.tfVar("k"); got != "from-secrets" {
		t.Errorf("k = %q", got)
	}
	if got := r.tfVar("n"); got != "3" {
		t.Errorf("n = %q", got)
	}
	writeFile(t, filepath.Join(proj, "tf/auto-vars/d.json"), `{"k": "from-auto-vars"}`, 0o644)
	if got := r.tfVar("k"); got != "from-auto-vars" {
		t.Errorf("k = %q, want tf/auto-vars before secrets/", got)
	}
}

// Shell case V: Clone marks the checkout as a snapshot tree, renames
// origin to infra, and leaves the worktree (the deploy archive) alone.
func TestCloneMarksSnapshotTree(t *testing.T) {
	bare := newBare(t)
	seedInfra(t, bare)
	proj := newProject(t)
	setRepoURL(t, proj, fileURL(bare))
	r, out := newRepo(proj)
	r.Clone(ctx())
	if v := configValue(proj, snapshotTreeKey); v != "true" {
		t.Fatalf("Clone did not set the mark:\n%s", out)
	}
	if _, err := gitErr(proj, "remote", "get-url", "infra"); err != nil {
		t.Errorf("no infra remote after Clone")
	}
	if _, err := gitErr(proj, "remote", "get-url", "origin"); err == nil {
		t.Errorf("origin was not renamed")
	}
	if fileExists(filepath.Join(proj, "infra.txt")) {
		t.Errorf("Clone checked infra/main out over the deploy archive")
	}
	if got := git(t, proj, "rev-parse", "--show-toplevel"); got != proj {
		t.Errorf("work tree = %q, want %q", got, proj)
	}
}
