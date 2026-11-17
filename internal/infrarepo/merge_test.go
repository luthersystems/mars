package infrarepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conflicted makes a project with an infra remote and a conflicted `git
// merge infra/main` in progress.
func conflicted(t *testing.T) (proj, bare string) {
	t.Helper()
	proj = newProject(t)
	bare = newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "base\n", 0o644)
	commitAll(t, proj, "shared base")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	theirConflict(t, bare)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "ours\n", 0o644)
	git(t, proj, "commit", "-q", "-am", "our edit")
	git(t, proj, "fetch", "-q", "infra", "main")
	_, _ = gitErr(proj, "merge", "-q", "infra/main")
	return proj, bare
}

func theirConflict(t *testing.T, bare string) {
	t.Helper()
	mate := filepath.Join(t.TempDir(), "mate")
	git(t, ".", "clone", "-q", "-b", "main", fileURL(bare), mate)
	writeFile(t, filepath.Join(mate, "conflict.txt"), "theirs\n", 0o644)
	git(t, mate, "commit", "-q", "-am", "their edit")
	git(t, mate, "push", "-q", "origin", "main")
}

// sharedProject is a project whose history is infra/main's (pushed there).
func sharedProject(t *testing.T) (proj, bare string) {
	t.Helper()
	proj = newProject(t)
	bare = newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	commitAll(t, proj, "shared base")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	return proj, bare
}

// Shell case N: apply.sh after a squash: the merge is skipped (rc 2), and
// the snapshot fast-forwards infra/main with the deployed tree.
func TestApplyAfterSquash(t *testing.T) {
	proj, bare := sharedProject(t)
	base := git(t, bare, "rev-parse", "main")
	if err := os.RemoveAll(filepath.Join(proj, ".git")); err != nil {
		t.Fatal(err)
	}
	initGit(t, proj)
	commitAll(t, proj, "ui-core: squashed project history")
	writeFile(t, filepath.Join(proj, "tf/custom-stack-provision/main.tf"), "simple-mode-change\n", 0o644)

	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), true); rc != MergeSkipped {
		t.Fatalf("Merge = %d, want 2 for unrelated histories:\n%s", rc, out)
	}
	if rc := r.Snapshot(ctx(), ""); rc != 0 {
		t.Fatalf("Snapshot = %d:\n%s", rc, out)
	}
	r.Push(ctx())
	if _, err := gitErr(bare, "merge-base", "--is-ancestor", base, "main"); err != nil {
		t.Errorf("not a fast-forward:\n%s", out)
	}
	if got := git(t, bare, "show", "main:tf/custom-stack-provision/main.tf"); got != "simple-mode-change" {
		t.Errorf("pushed main.tf = %q", got)
	}
}

// Shell case P: a stale checkout merges a teammate's edit before the apply,
// and the push keeps it.
func TestApplyMergesTeammatesFirst(t *testing.T) {
	proj, bare := sharedProject(t)
	mateHead := mateCommit(t, bare, "teammate.txt", "teammate change\n")

	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), true); rc != MergeOK || !fileExists(filepath.Join(proj, "teammate.txt")) {
		t.Fatalf("Merge = %d; the teammate's edit must be on disk before the apply:\n%s", rc, out)
	}
	if rc := r.Snapshot(ctx(), ""); rc != 0 {
		t.Fatalf("Snapshot = %d:\n%s", rc, out)
	}
	r.Push(ctx())
	if _, err := gitErr(bare, "merge-base", "--is-ancestor", mateHead, "main"); err != nil {
		t.Errorf("the push lost the teammate's commit")
	}
	if !hasPath(tree(t, bare, "main"), "teammate.txt") {
		t.Errorf("the teammate's file is gone from infra/main")
	}
}

// Shell case Q: a real conflict stops the apply and is aborted (no
// MERGE_HEAD, no markers, clean status).
func TestApplyMergeConflictAborts(t *testing.T) {
	proj, bare := sharedProject(t)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "base\n", 0o644)
	commitAll(t, proj, "base")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	theirConflict(t, bare)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "ours\n", 0o644)
	git(t, proj, "commit", "-q", "-am", "our edit")

	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), true); rc != MergeFailed {
		t.Fatalf("Merge = %d, want 1:\n%s", rc, out)
	}
	if fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) || readFile(t, filepath.Join(proj, "conflict.txt")) != "ours\n" ||
		git(t, proj, "status", "--porcelain", "--untracked-files=no") != "" {
		t.Errorf("the conflicted merge was left in place:\n%s", out)
	}
}

// Shell case Q: pull leaves the conflict to resolve, and commit concludes
// the resolved merge with a merge commit.
func TestPullLeavesConflictAndCommitConcludes(t *testing.T) {
	proj, bare := sharedProject(t)
	tpl := newBare(t)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "base\n", 0o644)
	commitAll(t, proj, "base")
	git(t, proj, "push", "-q", fileURL(bare), "main")
	git(t, proj, "push", "-q", fileURL(tpl), "main")
	git(t, proj, "remote", "add", "origin", fileURL(tpl))
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	theirConflict(t, bare)
	writeFile(t, filepath.Join(proj, "conflict.txt"), "ours\n", 0o644)
	git(t, proj, "commit", "-q", "-am", "our edit")

	r, out := newRepo(proj)
	if rc := r.Pull(ctx()); rc == 0 || !fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) ||
		!strings.Contains(readFile(t, filepath.Join(proj, "conflict.txt")), "<<<<<<<") {
		t.Fatalf("pull: rc=%d; the conflict must stay to resolve:\n%s", rc, out)
	}

	writeFile(t, filepath.Join(proj, "conflict.txt"), "resolved\n", 0o644)
	git(t, proj, "add", "conflict.txt")
	if rc := r.Commit(ctx(), "resolve"); rc != 0 {
		t.Fatalf("Commit = %d:\n%s", rc, out)
	}
	if fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) || len(strings.Fields(git(t, proj, "rev-list", "--parents", "-n1", "HEAD"))) != 3 {
		t.Errorf("the resolved merge was not concluded with a merge commit")
	}
}

// Shell case S1: an unborn HEAD skips the merge (rc 2).
func TestMergeUnbornHead(t *testing.T) {
	proj := newProject(t)
	bare := newBare(t)
	setRepoURL(t, proj, fileURL(bare))
	initGit(t, proj)
	git(t, proj, "remote", "add", "infra", fileURL(bare))
	seedInfra(t, bare)
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeSkipped || head(proj) != "" || fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) {
		t.Errorf("unborn HEAD: rc=%d\n%s", rc, out)
	}
}

// cloneProject clones bare as a project tree with an infra remote.
func cloneProject(t *testing.T, bare string, depth bool) string {
	t.Helper()
	proj := filepath.Join(t.TempDir(), "proj")
	args := []string{"clone", "-q", "-b", "main", "-o", "infra"}
	if depth {
		args = append(args, "--depth", "1")
	}
	git(t, ".", append(args, fileURL(bare), proj)...)
	git(t, proj, "config", "user.email", "t@l")
	git(t, proj, "config", "user.name", "T")
	return proj
}

// Shell case S2: uncommitted changes that do not overlap the merge stay,
// and the merge goes on.
func TestMergeKeepsNonOverlappingChanges(t *testing.T) {
	bare := newBare(t)
	seedInfra(t, bare)
	proj := cloneProject(t, bare, false)
	mateCommit(t, bare, "mate.txt", "mate\n")
	writeFile(t, filepath.Join(proj, "infra.txt"), "archive\n", 0o644)
	before := head(proj)
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeOK || !fileExists(filepath.Join(proj, "mate.txt")) ||
		head(proj) == before || readFile(t, filepath.Join(proj, "infra.txt")) != "archive\n" {
		t.Errorf("dirty worktree: rc=%d\n%s", rc, out)
	}
}

// shallowDiverged makes a depth-1 clone whose merge base with the new
// infra/main sits below the shallow cut.
func shallowDiverged(t *testing.T) string {
	t.Helper()
	bare := newBare(t)
	src := filepath.Join(t.TempDir(), "src")
	git(t, ".", "init", "-q", "-b", "main", src)
	for _, c := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(src, c+".txt"), c+"\n", 0o644)
		commitAll(t, src, c)
	}
	git(t, src, "push", "-q", fileURL(bare), "main")
	git(t, src, "push", "-q", fileURL(bare), "main:refs/heads/keep-c3")
	proj := cloneProject(t, bare, true)
	git(t, src, "checkout", "-q", "-b", "side", "HEAD~1")
	writeFile(t, filepath.Join(src, "c5.txt"), "c5\n", 0o644)
	commitAll(t, src, "c5")
	git(t, src, "push", "-q", "-f", fileURL(bare), "side:main")
	return proj
}

// Shell case S3: a shallow clone is deepened, then merged (not reported
// as unrelated).
func TestMergeDeepensShallowClone(t *testing.T) {
	proj := shallowDiverged(t)
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeOK || !fileExists(filepath.Join(proj, "c5.txt")) ||
		git(t, proj, "rev-parse", "--is-shallow-repository") != "false" {
		t.Errorf("shallow clone: rc=%d\n%s", rc, out)
	}
}

// Shell case S5: a failed deepen is an error (rc 1), not "unrelated".
func TestMergeFailedDeepen(t *testing.T) {
	proj := shallowDiverged(t)
	r, out := newRepo(proj, withFail(1, failGit("fetch", "--unshallow")))
	if rc := r.Merge(ctx(), false); rc != MergeFailed || !strings.Contains(strings.ToLower(out.String()), "deepen") {
		t.Errorf("failed deepen: rc=%d\n%s", rc, out)
	}
}

// Shell case S4: merge-base failing other than with exit 1 is an error.
func TestMergeBaseError(t *testing.T) {
	bare := newBare(t)
	seedInfra(t, bare)
	proj := cloneProject(t, bare, false)
	r, out := newRepo(proj, withFail(128, failGit("merge-base")))
	if rc := r.Merge(ctx(), false); rc != MergeFailed {
		t.Errorf("merge-base error: rc=%d\n%s", rc, out)
	}
}

// Shell case S6: a marked snapshot tree never merges and never fetches.
func TestMergeMarkedSnapshotTree(t *testing.T) {
	bare := newBare(t)
	seedInfra(t, bare)
	proj := cloneProject(t, bare, false)
	git(t, proj, "update-ref", "-d", "refs/remotes/infra/main")
	git(t, proj, "config", snapshotTreeKey, "true")
	mateCommit(t, bare, "mate.txt", "mate\n")
	before := head(proj)
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeSkipped || fileExists(filepath.Join(proj, "mate.txt")) || head(proj) != before {
		t.Errorf("marked tree: rc=%d\n%s", rc, out)
	}
	if _, err := gitErr(proj, "rev-parse", "-q", "--verify", "refs/remotes/infra/main"); err == nil {
		t.Errorf("the marked tree fetched infra/main")
	}
}

// Shell case T1: a merge in progress is refused, named, and not aborted.
func TestMergeRefusesMergeInProgress(t *testing.T) {
	proj, _ := conflicted(t)
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), true); rc != MergeFailed || !strings.Contains(out.String(), "git merge --abort") ||
		!fileExists(filepath.Join(proj, ".git/MERGE_HEAD")) {
		t.Errorf("merge in progress: rc=%d\n%s", rc, out)
	}
}

// Merge with no .git or no infra remote is a no-op (rc 0).
func TestMergeNoRemote(t *testing.T) {
	proj := newProject(t)
	r, _ := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeOK {
		t.Errorf("no .git: rc=%d", rc)
	}
	initGit(t, proj)
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeOK || !strings.Contains(out.String(), "no infra remote configured") {
		t.Errorf("no remote: rc=%d\n%s", rc, out)
	}
}

// infra/main absent (a new, empty -infra repo): nothing to merge (rc 0).
func TestMergeNoInfraMain(t *testing.T) {
	proj, _ := sharedProject(t)
	empty := newBare(t)
	setRepoURL(t, proj, fileURL(empty))
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeOK || !strings.Contains(out.String(), "does not exist yet") {
		t.Errorf("no infra/main: rc=%d\n%s", rc, out)
	}
}

// An unreachable infra remote fails the merge (rc 1).
func TestMergeFetchError(t *testing.T) {
	proj, _ := sharedProject(t)
	setRepoURL(t, proj, fileURL(filepath.Join(t.TempDir(), "gone.git")))
	r, out := newRepo(proj)
	if rc := r.Merge(ctx(), false); rc != MergeFailed || !strings.Contains(out.String(), "Could not fetch infra/main") {
		t.Errorf("fetch error: rc=%d\n%s", rc, out)
	}
}

// Shell case U1: pull in an -infra clone (no origin) skips the template
// merge and merges infra/main.
func TestPullWithoutOrigin(t *testing.T) {
	bare := newBare(t)
	seedInfra(t, bare)
	proj := cloneProject(t, bare, false)
	writeFile(t, filepath.Join(proj, "tf/auto-vars/common.auto.tfvars.json"), `{"repo_clone_ssh_url":"`+fileURL(bare)+`"}`, 0o644)
	writeFile(t, filepath.Join(proj, ".git/info/exclude"), "tf/auto-vars/\n", 0o644)
	mateCommit(t, bare, "mate.txt", "mate\n")
	git(t, proj, "commit", "-q", "--allow-empty", "-m", "local")
	r, out := newRepo(proj)
	if rc := r.Pull(ctx()); rc != 0 || !fileExists(filepath.Join(proj, "mate.txt")) ||
		!strings.Contains(strings.ToLower(out.String()), "no origin remote") {
		t.Errorf("-infra clone pull: rc=%d\n%s", rc, out)
	}
}

// Shell case U2: pull merges origin's branch main, not a tag named main.
func TestPullOriginBranchNotTag(t *testing.T) {
	tpl := newBare(t)
	src := filepath.Join(t.TempDir(), "tpl")
	git(t, ".", "init", "-q", "-b", "main", src)
	writeFile(t, filepath.Join(src, "base.txt"), "base\n", 0o644)
	commitAll(t, src, "base")
	git(t, src, "push", "-q", fileURL(tpl), "main")
	proj := filepath.Join(t.TempDir(), "proj")
	git(t, ".", "clone", "-q", "-b", "main", fileURL(tpl), proj)
	git(t, proj, "config", "user.email", "t@l")
	git(t, proj, "config", "user.name", "T")
	writeFile(t, filepath.Join(src, "origin.txt"), "origin\n", 0o644)
	commitAll(t, src, "template update")
	git(t, src, "push", "-q", fileURL(tpl), "main")
	git(t, src, "checkout", "-q", "-b", "side", "HEAD~1")
	writeFile(t, filepath.Join(src, "tagged.txt"), "tagged\n", 0o644)
	commitAll(t, src, "tagged, not on main")
	git(t, src, "tag", "main")
	git(t, src, "push", "-q", fileURL(tpl), "refs/tags/main")
	git(t, proj, "commit", "-q", "--allow-empty", "-m", "local")

	r, out := newRepo(proj)
	if rc := r.Pull(ctx()); rc != 0 || !fileExists(filepath.Join(proj, "origin.txt")) || fileExists(filepath.Join(proj, "tagged.txt")) {
		t.Errorf("origin tag main: rc=%d\n%s", rc, out)
	}
}
