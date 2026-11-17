package infrarepo

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// templateOnlyPaths belong to the template repo only and never go into a
// snapshot, so they never reach an -infra repo. .github/dependabot.yml
// configures Dependabot for the template; in an -infra repo it would open
// weekly PRs that the next deploy reverts. (The workflows ride along but
// guard themselves with `if: github.repository == ...`.) The files stay on
// disk, and a plain commit (commit.sh, destroy.sh) keeps them.
var templateOnlyPaths = []string{".github/dependabot.yml"}

// argBatch bounds the paths of one git command line, as xargs did.
const argBatch = 500

// gitBatched runs git with fixed args followed by paths, in batches.
func (r *Repo) gitBatched(ctx context.Context, fixed []string, paths []string) error {
	for len(paths) > 0 {
		n := min(len(paths), argBatch)
		args := append(append(append([]string{}, fixed...), "--"), paths[:n]...)
		if err := r.git(ctx, args...); err != nil {
			return err
		}
		paths = paths[n:]
	}
	return nil
}

// untrackIgnored stages the removal from git of every tracked file that the
// repo's .gitignore files ignore. The files stay on disk.
//
// Why (sandbox-infrastructure-template#160): .gitignore only stops git from
// ADDING an untracked file. A file that is already tracked stays tracked,
// and `git add -A` commits each new version of it. The template once
// tracked tf/auto-vars/common.auto.tfvars.json as a `{}` seed, so every
// project built from it committed the deploy-time cloud credentials ui-core
// writes there. This does not rewrite history.
//
// It fails closed: when it cannot list or untrack the files it returns an
// error, and nothing is staged or committed.
func (r *Repo) untrackIgnored(ctx context.Context) error {
	out, err := r.gitRaw(ctx, "ls-files", "-z", "--cached", "--ignored", "--exclude-per-directory=.gitignore")
	if err != nil {
		r.warn("⚠️  [git-infra] WARNING: git ls-files could not list the ignored files; not staging. [sandbox-infrastructure-template#160]")
		return err
	}
	files := splitZ(out)
	if len(files) == 0 {
		return nil
	}
	// The list is the record that these files were tracked, so pushed,
	// before: a credentials file among them needs rotating.
	r.warn("🔒 [git-infra] Untracking file(s) that .gitignore ignores (#160):")
	for _, f := range files {
		r.warn("    %s", f)
	}
	// --literal-pathspecs: the names are paths, not pathspecs, so a name
	// with * or [ ] untracks only itself.
	if err := r.gitBatched(ctx, []string{"--literal-pathspecs", "rm", "--cached", "--quiet", "--ignore-unmatch"}, files); err != nil {
		// Going on would commit the ignored files again (#160: the
		// deploy-time credentials file).
		r.warn("⚠️  [git-infra] WARNING: git rm --cached failed; not staging, so the ignored files are not committed. [sandbox-infrastructure-template#160]")
		return err
	}
	return nil
}

// indexPathsWithMode lists the index paths staged with mode (100644,
// 100755, ...).
func (r *Repo) indexPathsWithMode(ctx context.Context, mode string) ([]string, error) {
	out, err := r.gitRaw(ctx, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range splitZ(out) {
		// "<mode> <object> <stage>\t<path>"
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		if m, _, _ := strings.Cut(meta, " "); m == mode {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// isRegular reports a regular file that is not a symlink.
func isRegular(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular()
}

// isScript reports a regular file (not a symlink) whose first two bytes are
// "#!".
func isScript(path string) bool {
	if !isRegular(path) {
		return false
	}
	f, err := os.Open(path) //nolint:gosec // G304: a file of the project tree being staged
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return string(head) == "#!"
}

// executable reports a file (followed through a symlink) with an exec bit.
func executable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().Perm()&0o111 != 0
}

// markScriptsExecutable sets the exec bit in the index on each path staged
// as 100644 that is either
//
//   - one of keep and a regular file (not a symlink): the snapshot passes
//     the paths the project's own index had at 100755 before it reset onto
//     infra/main, so the project's modes win over infra/main's (this
//     repairs -infra repos that hold scripts as 100644); no script test,
//     since those modes were the project's choice; or
//   - new against base (every staged path when base is empty), executable
//     on disk and a script (isScript): core.fileMode is off, so `git add`
//     records a new 0755 script as 100644, and the script test keeps a new
//     data file at 100644 on a filesystem where everything reads as 0777.
//
// Other existing paths keep their index mode, and the bit is never removed:
// an archive or a mount can extract every file as 0777 (or drop the bit),
// and that must not churn modes. The cost: a 644->755 change of an existing
// script on disk alone is not carried (set it with `git update-index
// --chmod=+x`).
//
// Unlike the shell, a failed listing is an error, not "no paths": the
// staging fails closed rather than drop an exec bit silently.
func (r *Repo) markScriptsExecutable(ctx context.Context, base string, keep []string) error {
	staged, err := r.indexPathsWithMode(ctx, "100644")
	if err != nil {
		return err
	}
	staged644 := make(map[string]bool, len(staged))
	for _, p := range staged {
		staged644[p] = true
	}
	var paths []string
	for _, p := range keep {
		if staged644[p] && isRegular(filepath.Join(r.Root, p)) {
			paths = append(paths, p)
		}
	}
	var added string
	if base != "" {
		added, err = r.gitRaw(ctx, "diff", "--cached", "--name-only", "-z", "--no-renames", "--diff-filter=A", base)
	} else {
		added, err = r.gitRaw(ctx, "ls-files", "-z")
	}
	if err != nil {
		return err
	}
	for _, p := range splitZ(added) {
		full := filepath.Join(r.Root, p)
		if staged644[p] && executable(full) && isScript(full) {
			paths = append(paths, p)
		}
	}
	return r.gitBatched(ctx, []string{"update-index", "--chmod=+x"}, paths)
}

// stageTree stages the worktree for a commit, with the rules every commit
// of a project tree follows; commit and snapshot both use it, so the rules
// live in one place. base is the commit the index starts from (infra/main
// for a snapshot); empty means HEAD, or none on an unborn branch. Callers
// check for an unfinished operation first.
//
//  1. untrackIgnored, before `git add -A`: add -A commits new versions of
//     TRACKED files even when .gitignore matches them (#160).
//  2. `git add -A` with core.fileMode off, then markScriptsExecutable with
//     base and keep.
//  3. With dropTemplateOnly (only the snapshot), drop templateOnlyPaths
//     from the index.
func (r *Repo) stageTree(ctx context.Context, base string, dropTemplateOnly bool, keep []string) error {
	if base == "" {
		base = r.headCommit(ctx)
	}
	if err := r.untrackIgnored(ctx); err != nil {
		return err
	}
	if err := r.git(ctx, "add", "-A"); err != nil {
		return err
	}
	if err := r.markScriptsExecutable(ctx, base, keep); err != nil {
		return err
	}
	if dropTemplateOnly {
		args := append([]string{"rm", "--cached", "-q", "--ignore-unmatch", "--"}, templateOnlyPaths...)
		if err := r.git(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}
