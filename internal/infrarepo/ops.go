package infrarepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/luthersystems/mars/internal/runner"
)

// Exit codes of Merge.
const (
	MergeOK      = 0 // merged, nothing new, or no infra remote or infra/main yet
	MergeFailed  = 1 // refused or failed; nothing merged (see Merge)
	MergeSkipped = 2 // not merged on purpose: nothing to merge into
)

// snapshotTreeKey marks a checkout whose worktree is the deploy archive laid
// over an old infra/main commit (Clone). Merge never merges infra/main into
// it; the snapshot replaces infra/main instead.
const snapshotTreeKey = "luther.infraSnapshotTree"

// snapshotPendingKey is set by a snapshot that found no infra/main yet. The
// next Push consumes it and, when its push created infra/main from that
// snapshot, clears snapshotTreeKey.
const snapshotPendingKey = "luther.snapshotPending"

const (
	defaultCommitMessage  = "auto-commit: infrastructure changes [ci skip]"
	persistCommitMessage  = "auto-commit: deployed infrastructure [ci skip]"
	infraMainRefspec      = "+refs/heads/main:refs/remotes/infra/main"
	originMainRefspec     = "+refs/heads/main:refs/remotes/origin/main"
	infraMainRemoteBranch = "refs/heads/main"
)

// Clone gives a working tree without .git (an extracted project archive)
// the git state of the -infra repo, so that the commit and push steps work.
// The worktree is left as it is: it is the deploy archive, and the source of
// truth. Non-fatal: on any problem it warns and returns.
//
// It clones with --separate-git-dir straight into <root>/.git, which is
// what the shell's "clone to a temp dir, then mv .git" did, without a
// rename across filesystems.
func (r *Repo) Clone(ctx context.Context) {
	if r.hasGitDir() {
		return
	}
	url := r.remoteURL()
	if url == "" {
		r.warn("[git-infra] No repo_clone_ssh_url set; skipping infra clone")
		return
	}
	sshCmd := "ssh -o StrictHostKeyChecking=no"
	auth := r.authArgs()
	if len(auth) == 0 {
		key := r.deployKey()
		if st, err := os.Stat(key); err == nil && st.Mode().IsRegular() {
			_ = os.Chmod(key, 0o600)
			sshCmd = "ssh -i " + key + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=no"
		} else if strings.HasPrefix(url, "git@") {
			// SSH with no deploy key and no token: the clone can only fail.
			// A later step that has the token clones the repo (Persist).
			r.warn("[git-infra] No GitHub token or deploy key in this step; skipping infra clone")
			return
		}
	}

	r.warn("[git-infra] Cloning infra repo to initialize git state: %s", url)
	tmp, err := os.MkdirTemp("", "mars-infra-clone-")
	if err != nil {
		r.warn("⚠️  [git-infra] WARNING: failed to clone infra repo; git commit/push will be skipped")
		return
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	gitDir := filepath.Join(r.Root, ".git")
	args := append(append([]string{}, auth...), "clone", "--separate-git-dir="+gitDir, url, filepath.Join(tmp, "repo"))
	cmd := r.gitCmd(args, r.Stdout, r.Stderr)
	cmd.Env = map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_SSH_COMMAND": sshCmd}
	if err := r.Runner.Run(ctx, cmd); err != nil {
		// .git did not exist before, so whatever is there now is the
		// failed clone's.
		_ = os.RemoveAll(gitDir)
		r.warn("⚠️  [git-infra] WARNING: failed to clone infra repo; git commit/push will be skipped")
		return
	}

	if r.hasRemote(ctx, "origin") {
		_ = r.gitQuiet(ctx, "remote", "rename", "origin", "infra")
	}
	// The deploy archive now lies over an old infra/main commit and is the
	// source of truth. Mark the checkout so Merge never merges infra/main
	// content into it; the snapshot commit replaces infra/main.
	_ = r.gitQuiet(ctx, "config", snapshotTreeKey, "true")
	r.warn("[git-infra] Git state initialized from infra repo")
}

// fetchResult is the outcome of fetchInfraMain.
type fetchResult int

const (
	fetched fetchResult = iota
	absent              // the remote has no main yet: a new, empty -infra repo
	fetchError
)

// fetchInfraMain fetches infra/main into FETCH_HEAD and
// refs/remotes/infra/main. It names refs/heads/main in full both times:
// `git ls-remote --heads infra main` also matches a branch such as
// feature/main, and `git fetch infra main` takes a tag named main over the
// branch (refs/tags comes first), so the snapshot would sit on the wrong
// commit and its push be rejected.
func (r *Repo) fetchInfraMain(ctx context.Context) fetchResult {
	err := r.gitQuiet(ctx, "ls-remote", "--exit-code", "infra", infraMainRemoteBranch)
	switch runner.ExitCode(err) {
	case 0:
		if r.gitToStderr(ctx, "fetch", "infra", infraMainRefspec) != nil {
			return fetchError
		}
		return fetched
	case 2: // --exit-code: no matching ref
		return absent
	default:
		return fetchError
	}
}

// Merge merges infra/main into the project tree: pull, and apply before it
// applies, so a developer's stale checkout gets teammates' edits before
// Terraform runs. It behaves as a plain `git merge` (git refuses only local
// changes that overlap the merge), with these additions. It returns:
//
//	0  merged, nothing new to merge, or no infra remote or infra/main yet
//	   (or the project has no .git: nothing to merge into);
//	1  an operation (merge, rebase, cherry-pick, revert, sequencer) is in
//	   progress (refused, never aborted), infra/main could not be fetched,
//	   a shallow clone could not be deepened, merge-base failed, or the
//	   merge failed. With abortOnConflict (apply) the failed merge is
//	   aborted, so no MERGE_HEAD or conflict markers stay on disk for the
//	   snapshot to push; without it (pull) the conflict stays to resolve;
//	2  not merged: the checkout is marked luther.infraSnapshotTree (Clone
//	   laid the deploy archive over it), HEAD is unborn (cloned while infra
//	   was empty), or the histories are unrelated (ui-core squashes the
//	   project tree's history on every dispatch, so its tree never shares a
//	   commit with infra/main). Apply goes on, and its snapshot lands on top
//	   of infra/main (#160).
func (r *Repo) Merge(ctx context.Context, abortOnConflict bool) int {
	if !r.configure(ctx) {
		r.info("⚠️  configure_git failed, skipping git merge.")
		return MergeOK
	}
	if !r.hasRemote(ctx, "infra") {
		r.info("Skipping the infra/main merge: no infra remote configured")
		return MergeOK
	}
	// Refuse a merge (or rebase, ...) someone else started, and never abort
	// it below.
	if r.operationInProgress(ctx, scopeAll) {
		r.warn("⚠️  Not merging infra/main; finish or end the operation above first.")
		return MergeFailed
	}
	if r.configBool(ctx, snapshotTreeKey) {
		r.info("ℹ️  infra/main not merged: this checkout holds the deploy archive (infra-clone); the snapshot commit replaces infra/main.")
		return MergeSkipped
	}
	if r.headCommit(ctx) == "" {
		r.info("ℹ️  infra/main not merged: HEAD has no commit yet (cloned while infra was empty).")
		return MergeSkipped
	}

	r.info("🔄 Fetching infra/main…")
	switch r.fetchInfraMain(ctx) {
	case absent:
		r.info("ℹ️  infra/main does not exist yet (new repo) - skipping merge")
		return MergeOK
	case fetchError:
		r.warn("⚠️  Could not fetch infra/main.")
		return MergeFailed
	}

	// In a shallow clone the common ancestor can sit below the cut, and
	// merge-base would report none for related histories. A failed deepen
	// is an error: rc 2 would deploy a stale tree whose snapshot reverts a
	// teammate's commit.
	if out, _ := r.gitOut(ctx, "rev-parse", "--is-shallow-repository"); out == "true" {
		r.info("🔄 Deepening the shallow clone from infra…")
		if r.gitToStderr(ctx, "fetch", "--unshallow", "infra", infraMainRefspec) != nil {
			r.warn("⚠️  Could not deepen the shallow clone.")
			return MergeFailed
		}
	}

	// merge-base exits 1 only for "no common ancestor"; anything else is an
	// error, not unrelated histories.
	err := r.gitQuiet(ctx, "merge-base", "HEAD", "infra/main")
	switch code := runner.ExitCode(err); code {
	case 0:
	case 1:
		r.info("ℹ️  infra/main not merged: the project history and infra/main share no commit (ui-core's squashed tree).")
		return MergeSkipped
	default:
		r.warn("⚠️  git merge-base HEAD infra/main failed (exit %d).", code)
		return MergeFailed
	}

	r.info("🔀 Merging (no-ff, auto-edit) infra/main…")
	if r.git(ctx, "merge", "infra/main", "--no-ff", "--no-edit") == nil {
		r.info("✅ Merge commit created.")
		return MergeOK
	}
	r.warn("⚠️  Merge failed or conflicts detected.")
	if abortOnConflict && r.gitQuiet(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD") == nil {
		r.warn("   Aborting the merge, so no conflict is left for the snapshot to push.")
		_ = r.gitToStderr(ctx, "merge", "--abort")
	}
	return MergeFailed
}

// MergeOrigin merges the template's origin/main (pull.sh). A failed fetch
// returns git's exit code, a failed merge 1, and the conflict stays to
// resolve.
func (r *Repo) MergeOrigin(ctx context.Context) int {
	if !r.configure(ctx) {
		r.info("⚠️  configure_git failed, skipping git merge.")
		return 0
	}
	r.info("🔄 Fetching origin/main…")
	// The full ref: `git fetch origin main` takes a tag named main over the
	// branch.
	if err := r.git(ctx, "fetch", "origin", originMainRefspec); err != nil {
		return runner.ExitCode(err)
	}
	r.info("🔀 Merging (no‐ff, auto‐edit) origin/main…")
	if r.git(ctx, "merge", "origin/main", "--no-ff", "--no-edit") != nil {
		r.warn("⚠️  Merge failed or conflicts detected.")
		return 1
	}
	r.info("✅ Merge commit created.")
	return 0
}

// Pull is pull.sh: merge the template's origin/main when there is an
// origin remote (an -infra clone has none), then infra/main. Merge's rc 2
// (nothing to merge into) is not an error for a pull.
func (r *Repo) Pull(ctx context.Context) int {
	if r.hasGitDir() && r.hasRemote(ctx, "origin") {
		if rc := r.MergeOrigin(ctx); rc != 0 {
			return rc
		}
	} else {
		r.info("ℹ️  No origin remote (an -infra clone) - skipping the template merge.")
	}
	rc := r.Merge(ctx, false)
	if rc == MergeSkipped {
		r.info("ℹ️  Nothing pulled from infra/main.")
		return 0
	}
	return rc
}

// Commit is commit.sh's (and destroy.sh's) commit of the project tree. It
// returns 0 when it committed, found nothing to commit, or the project has
// no .git; 1 when it refused or a step failed, with nothing committed.
//
// Unresolved conflicts would commit their markers, and a commit in the
// middle of a rebase, cherry-pick or revert lands in the wrong place, so it
// refuses those. A resolved merge goes on: the commit concludes it, even
// when its result equals HEAD. It keeps the template-only paths; only the
// snapshot drops them.
func (r *Repo) Commit(ctx context.Context, msg string) int {
	if !r.configure(ctx) {
		r.info("⚠️  configure_git failed, skipping commit.")
		return 0
	}
	if msg == "" {
		msg = defaultCommitMessage
	}
	if r.operationInProgress(ctx, scopeCommit) {
		r.warn("⚠️  Nothing committed; finish or end the operation above first.")
		return 1
	}
	if r.stageTree(ctx, "", false, nil) != nil {
		r.warn("⚠️  Staging failed. Nothing committed.")
		return 1
	}
	code := runner.ExitCode(r.gitQuiet(ctx, "diff", "--cached", "--quiet"))
	if code > 1 {
		r.warn("⚠️  git diff --cached failed. Nothing committed.")
		return 1
	}
	if code == 0 && !r.stateExists(ctx, "MERGE_HEAD") {
		r.info("No changes to commit.")
		return 0
	}
	if r.git(ctx, "commit", "-m", msg) != nil {
		r.warn("⚠️  git commit failed. Nothing committed.")
		return 1
	}
	r.info("✅ Git commit created.")
	return 0
}

// Snapshot commits the deployed project tree as one snapshot commit on top
// of infra/main, so Push fast-forwards (sandbox-infrastructure-template#160,
// Phase 5). Apply and Persist call it after the apply.
//
// The -infra repo is an output copy of the deployed state; ui-core (its
// project archive) is the source of truth. So the snapshot is exactly the
// deployed tree: a file only infra/main has is removed (it stays in the
// history), and a file in both takes the deployed version. Keeping
// infra-only files would bring back what a deploy removes on purpose, such
// as a removed component's .tf.
//
// Why not a merge: ui-core squashes the project tree's git history on every
// dispatch, so from a project's second deploy on the local history and
// infra/main share no commit; `git merge` refuses unrelated histories, the
// push was rejected non-fast-forward, and every push after the first was
// lost.
//
// How: `git reset --mixed` onto infra/main moves HEAD and the index and
// leaves the worktree (the tree the apply just deployed); stageTree then
// stages it. Not --soft: --soft keeps a local index that can track an
// ignored file (the squashed tree of a project built from the old template
// tracks tf/auto-vars/common.auto.tfvars.json); that entry then differs
// from HEAD and from disk, `git rm --cached` refuses it, and `git add -A`
// commits the deploy-time credentials in it. With --mixed the index is
// infra/main's tree, and infra/main's modes stand for the files it has; the
// project's own 100755 paths, read before the reset, are set again.
//
// A missing infra/main (a new repo) skips the reset, and the first push
// creates main. It returns 0 when it committed, found nothing to commit or
// the project has no .git; 1 when it refused or a step failed (callers
// treat that as non-fatal).
func (r *Repo) Snapshot(ctx context.Context, msg string) int {
	if !r.configure(ctx) {
		r.info("⚠️  configure_git failed, skipping the snapshot commit.")
		return 0
	}
	if msg == "" {
		msg = defaultCommitMessage
	}
	// Before the reset: reset --mixed would drop MERGE_HEAD and the conflict
	// entries, and the markers on disk would then be committed; a rebase,
	// cherry-pick or revert in progress would be left pointing at a moved
	// HEAD.
	if r.operationInProgress(ctx, scopeAll) {
		r.warn("⚠️  [git-infra] WARNING: no snapshot; finish or end the operation above first. [sandbox-infrastructure-template#160]")
		return 1
	}
	// A pending mark from an earlier run must not let this run's push clear
	// luther.infraSnapshotTree; only this run's snapshot sets it again.
	r.unsetConfig(ctx, snapshotPendingKey)

	base := ""
	var keep []string
	hasInfra := r.hasRemote(ctx, "infra")
	if hasInfra {
		r.info("🔄 Fetching infra/main…")
		switch r.fetchInfraMain(ctx) {
		case fetched:
			var err error
			if base, err = r.gitOut(ctx, "rev-parse", "FETCH_HEAD"); err != nil || base == "" {
				r.warn("⚠️  [git-infra] WARNING: could not read the fetched infra/main. [sandbox-infrastructure-template#160]")
				return 1
			}
			// The project tree's own 100755 paths, before the reset
			// replaces the index with infra/main's modes.
			if keep, err = r.indexPathsWithMode(ctx, "100755"); err != nil {
				r.warn("⚠️  [git-infra] WARNING: could not read the index modes. [sandbox-infrastructure-template#160]")
				return 1
			}
			r.info("📸 Re-parenting the deployed tree on infra/main (snapshot commit)…")
			if r.git(ctx, "reset", "--quiet", "--mixed", base) != nil {
				r.warn("⚠️  [git-infra] WARNING: git reset --mixed onto infra/main failed. [sandbox-infrastructure-template#160]")
				return 1
			}
		case absent:
			r.info("ℹ️  infra/main does not exist yet (new repo) - the first push creates it")
		default:
			r.warn("⚠️  [git-infra] WARNING: could not fetch infra/main. [sandbox-infrastructure-template#160]")
			return 1
		}
	}
	if r.stageTree(ctx, base, true, keep) != nil {
		r.warn("⚠️  [git-infra] WARNING: staging the snapshot failed; no snapshot commit. [sandbox-infrastructure-template#160]")
		return 1
	}

	switch code := runner.ExitCode(r.gitQuiet(ctx, "diff", "--cached", "--quiet")); {
	case code == 0:
		r.info("No changes to commit.")
	case code == 1 && r.git(ctx, "commit", "-q", "-m", msg) == nil:
		r.info("✅ Snapshot commit created.")
	default:
		r.warn("⚠️  [git-infra] WARNING: the snapshot commit failed. [sandbox-infrastructure-template#160]")
		return 1
	}
	// HEAD now sits on infra/main, so the histories are related: later
	// applies and pulls in this checkout merge teammates' commits normally.
	// Drop Clone's mark, only now that the snapshot succeeded. With no
	// infra/main yet, Push drops it once its push creates main; with no
	// infra remote, nothing is related, so it stays.
	if base != "" {
		r.unsetConfig(ctx, snapshotTreeKey)
	} else if hasInfra {
		_ = r.gitQuiet(ctx, "config", snapshotPendingKey, "true")
	}
	return 0
}

// Push pushes HEAD to the infra remote. It never fails: a push problem must
// not turn an already-successful deploy into a failure, so every skip and
// failure is a loud, greppable "[git-infra] WARNING:" instead (#143: a
// silent skip left 89/89 -infra repos empty).
func (r *Repo) Push(ctx context.Context) {
	if !r.hasGitDir() || !r.hasRemote(ctx, "infra") {
		r.warn("⚠️  [git-infra] WARNING: no 'infra' remote configured — NOT pushing; the per-project -infra repo will stay EMPTY. [sandbox-infrastructure-template#143]")
		return
	}
	head := r.headCommit(ctx)
	if head == "" {
		r.warn("⚠️  [git-infra] WARNING: no HEAD/commit to push (nothing was committed) — the per-project -infra repo will stay EMPTY. [sandbox-infrastructure-template#143]")
		return
	}
	// Set by a snapshot that found no infra/main; consumed here whatever the
	// push does.
	pending := r.configBool(ctx, snapshotPendingKey)
	r.unsetConfig(ctx, snapshotPendingKey)

	r.info("🚀 [git-infra] Pushing HEAD to infra remote…")
	if r.git(ctx, "push", "infra", "HEAD") != nil {
		r.warn("⚠️  [git-infra] WARNING: 'git push infra HEAD' FAILED — the per-project -infra repo may stay EMPTY. Deploy is NOT failed (apply already succeeded); investigate the GitHub token or deploy key and the remote URL. [sandbox-infrastructure-template#143]")
		return
	}
	r.info("✅ [git-infra] Pushed to infra remote — per-project -infra repo populated.")
	// The push created infra/main from this run's snapshot, so the checkout
	// is now related to it: drop Clone's mark. Not after any other push (an
	// up-to-date push after a failed snapshot proves nothing).
	if pending {
		out, err := r.gitOut(ctx, "ls-remote", "infra", infraMainRemoteBranch)
		if remote, _, _ := strings.Cut(out, "\t"); err == nil && remote == head {
			r.unsetConfig(ctx, snapshotTreeKey)
		}
	}
}

// Persist commits the applied working tree and pushes it to the
// per-project -infra repo: the custom-stack apply paths that production
// runs (apply-with-outputs.sh --check-drift, apply-plan.sh) call it, since
// they never reach apply.sh's push (#143). It clones the repo first when
// the tree has no .git: with the GitHub App, prepare-custom-stack has no
// token, so this step, which has one, clones it.
//
// Contract: non-fatal and loud. It never fails; every skip and failure is a
// greppable "[git-infra] WARNING:".
func (r *Repo) Persist(ctx context.Context) {
	r.info("📦 [git-infra] Persisting applied stack to the per-project -infra repo…")
	r.Clone(ctx)
	if !r.hasGitDir() {
		r.warn("⚠️  [git-infra] WARNING: no .git at %s at persist time — infra-clone (prepare-custom-stack, or this step) could not clone the -infra repo. Stack will NOT be pushed. [sandbox-infrastructure-template#143]", r.Root)
	}
	if r.tfVar(repoURLVar) == "" {
		r.warn("⚠️  [git-infra] WARNING: repo_clone_ssh_url is empty at persist time — cloud-provision's git_repo.auto.tfvars.json did not reach the apply working tree. [sandbox-infrastructure-template#143]")
	}
	if r.Snapshot(ctx, persistCommitMessage) != 0 {
		r.warn("⚠️  [git-infra] WARNING: the snapshot commit returned non-zero (continuing; non-fatal). [sandbox-infrastructure-template#160]")
	}
	r.Push(ctx)
}
