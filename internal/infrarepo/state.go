package infrarepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// scope says which unfinished git operations block a step.
type scope int

const (
	// scopeCommit is a plain commit's view (commit.sh, destroy.sh): a
	// resolved merge is allowed, because the commit concludes it, and a
	// sequencer/ directory counts only with CHERRY_PICK_HEAD or REVERT_HEAD,
	// because `git reset --hard` of a multi-pick leaves sequencer/ behind.
	scopeCommit scope = iota
	// scopeAll is the view of the snapshot, which resets the index, and of
	// the merge: every state counts, sequencer/ alone too.
	scopeAll
)

// The entries of the git directory that mark an unfinished operation.
var stateNames = []string{"MERGE_HEAD", "REBASE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"}

// gitPaths resolves names inside the git directory with one rev-parse
// call. ok is false when rev-parse fails or prints an unexpected number of
// lines; callers then fail closed.
func (r *Repo) gitPaths(ctx context.Context, names []string) (paths []string, ok bool, why string) {
	args := []string{"rev-parse"}
	for _, n := range names {
		args = append(args, "--git-path", n)
	}
	out, err := r.gitOut(ctx, args...)
	if err != nil {
		return nil, false, "git rev-parse --git-path failed"
	}
	lines := strings.Split(out, "\n")
	if len(lines) != len(names) {
		return nil, false, "unexpected git rev-parse output"
	}
	for i, p := range lines {
		if !filepath.IsAbs(p) {
			lines[i] = filepath.Join(r.Root, p)
		}
	}
	return lines, true, ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// stateExists reports whether any of the named git directory entries
// exists. An unreadable state counts as present (fail closed).
func (r *Repo) stateExists(ctx context.Context, names ...string) bool {
	paths, ok, why := r.gitPaths(ctx, names)
	if !ok {
		r.warn("⚠️  Could not read the git state (%s); treating it as an operation in progress.", why)
		return true
	}
	for _, p := range paths {
		if exists(p) {
			return true
		}
	}
	return false
}

// unresolved is the template's unresolvedMerge: 0 when the index has
// unmerged (conflicted) entries, 1 when it has none, 2 when `git ls-files
// -u` fails (callers fail closed). A resolved merge (MERGE_HEAD and no
// unmerged entries) has none.
func (r *Repo) unresolved(ctx context.Context) int {
	out, err := r.gitOut(ctx, "ls-files", "-u")
	if err != nil {
		return 2
	}
	if out != "" {
		return 0
	}
	return 1
}

// operationInProgress reports whether git is in the middle of an operation
// that the step must not run over, and prints, for each, how to end it. It
// fails closed: an unreadable state is an operation in progress.
func (r *Repo) operationInProgress(ctx context.Context, sc scope) bool {
	paths, ok, why := r.gitPaths(ctx, stateNames)
	if !ok {
		r.warn("⚠️  Could not read the git state (%s); treating it as an operation in progress.", why)
		return true
	}
	has := map[string]bool{}
	for i, n := range stateNames {
		has[n] = exists(paths[i])
	}

	var found []string
	switch r.unresolved(ctx) {
	case 2:
		found = append(found, "unknown: could not read the git index (git ls-files -u failed)")
	case 0:
		found = append(found, "unresolved merge conflicts: resolve them (git add) and commit, or run git merge --abort")
	default:
		if has["MERGE_HEAD"] && sc == scopeAll {
			found = append(found, "a merge: commit it (commit.sh), or run git merge --abort")
		}
	}
	if has["REBASE_HEAD"] || has["rebase-merge"] || has["rebase-apply"] {
		found = append(found, "a rebase: git rebase --continue, or git rebase --abort")
	}
	if has["CHERRY_PICK_HEAD"] {
		found = append(found, "a cherry-pick: git cherry-pick --continue, or git cherry-pick --quit")
	}
	if has["REVERT_HEAD"] {
		found = append(found, "a revert: git revert --continue, or git revert --quit")
	}
	if has["sequencer"] && sc == scopeAll && !has["CHERRY_PICK_HEAD"] && !has["REVERT_HEAD"] {
		found = append(found, "a paused multi-commit cherry-pick or revert (sequencer/): git cherry-pick --quit or git revert --quit")
	}
	for _, f := range found {
		r.warn("   In progress: %s.", f)
	}
	return len(found) > 0
}
