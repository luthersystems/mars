package infrarepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Shell case R: a plain commit (commit.sh, destroy.sh) keeps the
// template-only paths and records a new 0755 script as 100755.
func TestCommitKeepsTemplateOnlyAndExecBit(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, ".github/dependabot.yml"), "version: 2\n", 0o644)
	writeFile(t, filepath.Join(proj, "r-new-hook.sh"), "#!/bin/sh\n", 0o755)
	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 0 {
		t.Fatalf("Commit = %d:\n%s", rc, out)
	}
	if !hasPath(tree(t, proj, "HEAD"), ".github/dependabot.yml") {
		t.Errorf("Commit dropped .github/dependabot.yml")
	}
	if m := mode(t, proj, "HEAD", "r-new-hook.sh"); m != "100755" {
		t.Errorf("new 0755 script is %s", m)
	}
}

// Shell case R: +x only for NEW paths that are executable on disk and
// start with "#!"; existing paths keep their index mode.
func TestCommitModeRules(t *testing.T) {
	proj := newProject(t)
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, "was-644.sh"), "#!/bin/sh\n", 0o644)
	writeFile(t, filepath.Join(proj, "was-755.sh"), "#!/bin/sh\n", 0o644)
	writeFile(t, filepath.Join(proj, "data.tf"), `resource "x" "y" {}`+"\n", 0o644)
	writeFile(t, filepath.Join(proj, "data.json"), "{}\n", 0o644)
	git(t, proj, "add", "was-644.sh", "was-755.sh", "data.tf", "data.json")
	git(t, proj, "update-index", "--chmod=+x", "was-755.sh")
	git(t, proj, "commit", "-q", "-m", "base")
	writeFile(t, filepath.Join(proj, "was-644.sh"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(proj, "was-755.sh"), "#!/bin/sh\n", 0o644)
	writeFile(t, filepath.Join(proj, "data.tf"), `resource "x" "y" {}`+"\n", 0o777)
	writeFile(t, filepath.Join(proj, "data.json"), "{}\nchange\n", 0o777)
	writeFile(t, filepath.Join(proj, "new-script.sh"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(proj, "new-data.tf"), "x = 1\n", 0o777)

	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 0 {
		t.Fatalf("Commit = %d:\n%s", rc, out)
	}
	for path, want := range map[string]string{
		"new-script.sh": "100755", // new, 0755, a script
		"was-644.sh":    "100644", // existing: the index mode stands
		"data.tf":       "100644", // 0777 data stays
		"data.json":     "100644",
		"new-data.tf":   "100644", // new 0777 data is no script
		"was-755.sh":    "100755", // the bit is never removed
	} {
		if m := mode(t, proj, "HEAD", path); m != want {
			t.Errorf("%s is %s, want %s", path, m, want)
		}
	}
}

// Shell case T2: Commit refuses a conflicted merge in progress.
func TestCommitRefusesConflict(t *testing.T) {
	proj, _ := conflicted(t)
	before := head(proj)
	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 1 || head(proj) != before {
		t.Errorf("conflict: rc=%d\n%s", rc, out)
	}
}

// Shell case T3: the snapshot refuses a merge in progress before its reset.
func TestSnapshotRefusesConflict(t *testing.T) {
	proj, _ := conflicted(t)
	before := head(proj)
	r, out := newRepo(proj)
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 1 || head(proj) != before || !fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) {
		t.Errorf("conflict: rc=%d\n%s", rc, out)
	}
}

// Shell case T4: the snapshot carries the project index's 100755 (script
// or data) over infra/main's 100644; 0777 data from disk stays 100644.
func TestSnapshotCarriesProjectExecBits(t *testing.T) {
	bare := newBare(t)
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, ".", "init", "-q", "-b", "main", seed)
	writeFile(t, filepath.Join(seed, "tf/apply.sh"), "#!/bin/sh\n", 0o644)
	writeFile(t, filepath.Join(seed, "data.tf"), "x = 1\n", 0o644)
	writeFile(t, filepath.Join(seed, "data.json"), "{}\n", 0o644)
	commitAll(t, seed, "infra: scripts as 100644")
	git(t, seed, "push", "-q", fileURL(bare), "main")

	proj := newProject(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, "tf/apply.sh"), "#!/bin/sh\n", 0o644)
	writeFile(t, filepath.Join(proj, "data.tf"), "x = 1\n", 0o644)
	writeFile(t, filepath.Join(proj, "data.json"), "{}\n", 0o644)
	git(t, proj, "add", "-A")
	git(t, proj, "update-index", "--chmod=+x", "tf/apply.sh", "data.json")
	git(t, proj, "commit", "-q", "-m", "project: apply.sh 100755")
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	writeFile(t, filepath.Join(proj, "new.json"), "{}\n", 0o777)
	writeFile(t, filepath.Join(proj, "tf/apply.sh"), "#!/bin/sh\n", 0o777)
	writeFile(t, filepath.Join(proj, "data.tf"), "x = 1\n", 0o777)

	r, out := newRepo(proj)
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 0 {
		t.Fatalf("Snapshot = %d:\n%s", rc, out)
	}
	for path, want := range map[string]string{"tf/apply.sh": "100755", "data.tf": "100644", "data.json": "100755", "new.json": "100644"} {
		if m := mode(t, proj, "HEAD", path); m != want {
			t.Errorf("%s is %s, want %s", path, m, want)
		}
	}
	if git(t, proj, "rev-parse", "HEAD^") != git(t, bare, "rev-parse", "main") {
		t.Errorf("the snapshot is not on top of infra/main")
	}
}

// Shell case T5: a stopped rebase blocks the snapshot (naming the fix)
// and a plain commit.
func TestRebaseInProgressBlocks(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	commitAll(t, proj, "one")
	writeFile(t, filepath.Join(proj, "two.txt"), "two\n", 0o644)
	commitAll(t, proj, "two")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	cmd := exec.Command("git", "-c", "user.email=t@l", "-c", "user.name=T", "rebase", "-q", "-i", "HEAD~1")
	cmd.Dir = proj
	cmd.Env = append(cmd.Environ(), "GIT_SEQUENCE_EDITOR=sed -i 1s/^pick/edit/")
	_ = cmd.Run()
	if !fileExists(filepath.Join(proj, ".git/rebase-merge")) {
		t.Fatal("fixture: no rebase in progress")
	}
	before := head(proj)

	r, out := newRepo(proj)
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 1 || !strings.Contains(out.String(), "git rebase --abort") || head(proj) != before {
		t.Errorf("snapshot during a rebase: rc=%d\n%s", rc, out)
	}
	writeFile(t, filepath.Join(proj, "edit.txt"), "edit\n", 0o644)
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 1 || head(proj) != before {
		t.Errorf("commit during a rebase: rc=%d\n%s", rc, out)
	}
}

// Shell case T8: a leftover sequencer/ (after `git reset --hard` of a
// multi-pick) blocks the snapshot, naming the fix; a plain commit ignores
// it without CHERRY_PICK_HEAD or REVERT_HEAD.
func TestLeftoverSequencer(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, "pick.txt"), "base\n", 0o644)
	commitAll(t, proj, "base")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	git(t, proj, "checkout", "-q", "-b", "side")
	writeFile(t, filepath.Join(proj, "pick.txt"), "side1\n", 0o644)
	commitAll(t, proj, "side1")
	writeFile(t, filepath.Join(proj, "side2.txt"), "side2\n", 0o644)
	commitAll(t, proj, "side2")
	git(t, proj, "checkout", "-q", "main")
	writeFile(t, filepath.Join(proj, "pick.txt"), "main1\n", 0o644)
	commitAll(t, proj, "main1")
	_, _ = gitErr(proj, "cherry-pick", "side~1", "side")
	_ = os.Remove(filepath.Join(proj, ".git/CHERRY_PICK_HEAD"))
	git(t, proj, "reset", "-q", "--hard")
	if !fileExists(filepath.Join(proj, ".git/sequencer")) {
		t.Fatal("fixture: no sequencer/ left")
	}
	before := head(proj)
	writeFile(t, filepath.Join(proj, "edit.txt"), "edit\n", 0o644)

	r, out := newRepo(proj)
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 1 || !strings.Contains(out.String(), "git cherry-pick --quit") || head(proj) != before {
		t.Errorf("leftover sequencer, snapshot: rc=%d\n%s", rc, out)
	}
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 0 || head(proj) == before {
		t.Errorf("leftover sequencer, commit: rc=%d\n%s", rc, out)
	}
}

// Shell cases T6 and T7: the commit destroy.sh makes after a destroy fails
// (rc 1, which destroy.sh maps to 75) on a conflict or a failing hook.
func TestCommitFailsOnHook(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	commitAll(t, proj, "base")
	writeFile(t, filepath.Join(proj, ".git/hooks/pre-commit"), "#!/bin/sh\nexit 1\n", 0o755)
	writeFile(t, filepath.Join(proj, "change.txt"), "change\n", 0o644)
	before := head(proj)
	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), ""); rc != 1 || head(proj) != before || !strings.Contains(out.String(), "git commit failed") {
		t.Errorf("failing hook: rc=%d\n%s", rc, out)
	}
}

// Shell case T9: the mark is cleared only after a successful snapshot, and,
// with no infra/main yet, only by the push that creates it; with no infra
// remote it stays.
func TestSnapshotMarkLifecycle(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	git(t, proj, "config", snapshotTreeKey, "true")
	hook := filepath.Join(proj, ".git/hooks/pre-commit")
	writeFile(t, hook, "#!/bin/sh\nexit 1\n", 0o755)

	r, out := newRepo(proj)
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 1 || configValue(proj, snapshotTreeKey) != "true" {
		t.Errorf("failed snapshot commit: rc=%d mark=%q\n%s", rc, configValue(proj, snapshotTreeKey), out)
	}
	_ = os.Remove(hook)
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 0 || head(proj) == "" || configValue(proj, snapshotTreeKey) != "true" {
		t.Errorf("first snapshot (no infra/main) must keep the mark until the push: rc=%d\n%s", rc, out)
	}
	r.Push(ctx())
	if head(bare) == "" || configValue(proj, snapshotTreeKey) != "" {
		t.Errorf("the push that creates infra/main must clear the mark:\n%s", out)
	}

	noRemote := newProject(t)
	initGit(t, noRemote)
	git(t, noRemote, "config", snapshotTreeKey, "true")
	r2, out2 := newRepo(noRemote)
	r2.Snapshot(ctx(), "snapshot")
	r2.Push(ctx())
	if configValue(noRemote, snapshotTreeKey) != "true" {
		t.Errorf("without an infra remote the mark must stay:\n%s", out2)
	}
}

// Shell case T14: a failed snapshot followed by an up-to-date push keeps
// the mark.
func TestFailedSnapshotThenPushKeepsMark(t *testing.T) {
	proj, bare := sharedProject(t)
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	git(t, proj, "config", snapshotTreeKey, "true")
	writeFile(t, filepath.Join(proj, "t14.txt"), "change\n", 0o644)
	writeFile(t, filepath.Join(proj, ".git/hooks/pre-commit"), "#!/bin/sh\nexit 1\n", 0o755)
	r, out := newRepo(proj)
	r.Snapshot(ctx(), "snapshot")
	r.Push(ctx())
	if configValue(proj, snapshotTreeKey) != "true" {
		t.Errorf("the mark was cleared after a failed snapshot:\n%s", out)
	}
}

// Shell case T10: a staging failure (git add) stops the snapshot before it
// commits; HEAD stays on infra/main's base and the mark stays.
func TestSnapshotStagingFailure(t *testing.T) {
	bare := newBare(t)
	base := seedInfra(t, bare)
	proj := newProject(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	git(t, proj, "config", snapshotTreeKey, "true")
	r, out := newRepo(proj, withFail(1, failGit("add")))
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 1 || head(proj) != base || configValue(proj, snapshotTreeKey) != "true" {
		t.Errorf("staging failure: rc=%d head=%s\n%s", rc, head(proj), out)
	}
}

// Shell case T11: an unreadable git state fails closed.
func TestSnapshotUnreadableStateFailsClosed(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	r, out := newRepo(proj, withFail(1, failGit("rev-parse", "--git-path")))
	if rc := r.Snapshot(ctx(), "snapshot"); rc != 1 || !strings.Contains(strings.ToLower(out.String()), "could not read the git state") || head(proj) != "" {
		t.Errorf("failing rev-parse: rc=%d\n%s", rc, out)
	}
}

// trackedSecret is a project that tracks an ignored file that now holds a
// deploy-time secret.
func trackedSecret(t *testing.T) string {
	t.Helper()
	proj := newProject(t)
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, ".gitignore"), "secret.txt\n", 0o644)
	writeFile(t, filepath.Join(proj, "secret.txt"), "old\n", 0o644)
	git(t, proj, "add", "-A")
	git(t, proj, "add", "-f", "secret.txt")
	git(t, proj, "commit", "-q", "-m", "base")
	writeFile(t, filepath.Join(proj, "secret.txt"), "deploy-time-secret\n", 0o644)
	return proj
}

// Shell case T12: a failing `git rm --cached` stops the commit, so the
// tracked ignored file is not committed again.
func TestCommitFailedUntrack(t *testing.T) {
	proj := trackedSecret(t)
	before := head(proj)
	r, out := newRepo(proj, withFail(1, failGit("rm")))
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 1 || head(proj) != before ||
		strings.Contains(git(t, proj, "log", "-p"), "deploy-time-secret") {
		t.Errorf("failed untrack: rc=%d\n%s", rc, out)
	}
	// And without the failure, the file is untracked and stays on disk.
	r, out = newRepo(proj)
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 0 || hasPath(tree(t, proj, "HEAD"), "secret.txt") ||
		strings.Contains(git(t, proj, "log", "-p"), "deploy-time-secret") || !fileExists(filepath.Join(proj, "secret.txt")) {
		t.Errorf("untrack: rc=%d\n%s", rc, out)
	}
}

// A name with pathspec magic untracks only itself (--literal-pathspecs).
func TestUntrackLiteralPathspec(t *testing.T) {
	proj := newProject(t)
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, ".gitignore"), "ignored*\n", 0o644)
	writeFile(t, filepath.Join(proj, "ignored[1].txt"), "x\n", 0o644)
	writeFile(t, filepath.Join(proj, "keep1.txt"), "k\n", 0o644)
	git(t, proj, "add", "-A")
	git(t, proj, "add", "-f", "ignored[1].txt")
	git(t, proj, "commit", "-q", "-m", "base")
	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), ""); rc != 0 {
		t.Fatalf("Commit = %d:\n%s", rc, out)
	}
	paths := tree(t, proj, "HEAD")
	if hasPath(paths, "ignored[1].txt") || !hasPath(paths, "keep1.txt") {
		t.Errorf("tree after untrack: %v", paths)
	}
}

// Shell case T15: a failed ls-files in the untrack stops the commit.
func TestCommitFailedIgnoredListing(t *testing.T) {
	proj := newProject(t)
	initGit(t, proj)
	commitAll(t, proj, "base")
	writeFile(t, filepath.Join(proj, "t15.txt"), "change\n", 0o644)
	before := head(proj)
	r, out := newRepo(proj, withFail(1, failGit("ls-files", "--ignored")))
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 1 || head(proj) != before {
		t.Errorf("failed ls-files --ignored: rc=%d\n%s", rc, out)
	}
}

// Shell case T16: a failed `git ls-files -u` says the index could not be
// read, not that there are conflicts.
func TestCommitUnreadableIndex(t *testing.T) {
	proj := newProject(t)
	initGit(t, proj)
	commitAll(t, proj, "base")
	writeFile(t, filepath.Join(proj, "t16.txt"), "change\n", 0o644)
	r, out := newRepo(proj, withFail(1, failGit("ls-files", "-u")))
	if rc := r.Commit(ctx(), "commit.sh run"); rc != 1 || !strings.Contains(out.String(), "could not read the git index") ||
		strings.Contains(out.String(), "resolve them") {
		t.Errorf("failed ls-files -u: rc=%d\n%s", rc, out)
	}
}

// A resolved merge whose result equals HEAD still needs its commit.
func TestCommitConcludesEmptyResolvedMerge(t *testing.T) {
	proj, _ := conflicted(t)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "ours\n", 0o644)
	git(t, proj, "add", "conflict.txt")
	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), "resolve"); rc != 0 || fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) {
		t.Errorf("empty resolved merge: rc=%d\n%s", rc, out)
	}
}

// Commit with nothing staged says so and succeeds; with no .git it skips.
func TestCommitNothingAndNoGit(t *testing.T) {
	proj := newProject(t)
	r, out := newRepo(proj)
	if rc := r.Commit(ctx(), ""); rc != 0 || !strings.Contains(out.String(), "configure_git failed, skipping commit") {
		t.Errorf("no .git: rc=%d\n%s", rc, out)
	}
	initGit(t, proj)
	commitAll(t, proj, "base")
	r, out = newRepo(proj)
	if rc := r.Commit(ctx(), ""); rc != 0 || !strings.Contains(out.String(), "No changes to commit.") {
		t.Errorf("nothing to commit: rc=%d\n%s", rc, out)
	}
	if msg := git(t, proj, "log", "-1", "--format=%s"); msg != "base" {
		t.Errorf("an empty commit was made: %q", msg)
	}
}
