// Package infrarepo keeps a project tree's per-project "-infra" git repo in
// step with what was deployed. It is the git logic of
// sandbox-infrastructure-template's shell_utils.sh (cloneInfraRepo,
// gitMergeInfraMain, gitCommit, gitSnapshotCommit, gitPushInfra,
// persistInfraRepo and their helpers), moved to Go so it can be tested
// with ordinary Go tests and read in one place (template#190, #160, #143).
//
// It runs the git CLI through mars's runner, not a Go git library: the
// rules here are stated in terms of git's own commands (merge, reset
// --mixed, ls-files --ignored, update-index --chmod), and the mars image
// ships git, so the behaviour is git's, byte for byte, including hooks,
// credential helpers and core.sshCommand.
//
// docs/infra-repo.md describes the commands and their exit codes.
package infrarepo

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luthersystems/mars/internal/runner"
)

// Repo is a project tree: a directory that may hold a .git, whose "infra"
// remote is the per-project -infra repo.
type Repo struct {
	// Root is the project root (the template's MARS_PROJECT_ROOT).
	Root   string
	Runner runner.Runner
	// Stdout gets the progress lines, Stderr the warnings and errors, as
	// the shell functions printed them.
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads the GitHub token file variables; os.Getenv when nil.
	Getenv func(string) string
}

func (r *Repo) getenv(key string) string {
	if r.Getenv != nil {
		return r.Getenv(key)
	}
	return os.Getenv(key)
}

func (r *Repo) info(format string, args ...any) {
	fmt.Fprintf(r.Stdout, format+"\n", args...)
}

func (r *Repo) warn(format string, args ...any) {
	fmt.Fprintf(r.Stderr, format+"\n", args...)
}

func (r *Repo) gitCmd(args []string, stdout, stderr io.Writer) runner.Command {
	cmd := runner.Cmd("git", args...)
	cmd.Dir = r.Root
	cmd.Quiet = true
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd
}

// git runs git in the project root; its output goes where the shell's did:
// stdout to Stdout, stderr to Stderr.
func (r *Repo) git(ctx context.Context, args ...string) error {
	return r.Runner.Run(ctx, r.gitCmd(args, r.Stdout, r.Stderr))
}

// gitToStderr runs git with its stdout on Stderr (the shell's `>&2`), so
// fetch and abort chatter never mixes with a caller's captured stdout.
func (r *Repo) gitToStderr(ctx context.Context, args ...string) error {
	return r.Runner.Run(ctx, r.gitCmd(args, r.Stderr, r.Stderr))
}

// gitQuiet runs git with all its output dropped (`>/dev/null 2>&1`).
func (r *Repo) gitQuiet(ctx context.Context, args ...string) error {
	return r.Runner.Run(ctx, r.gitCmd(args, io.Discard, io.Discard))
}

// gitOut captures git's stdout, without the trailing newlines (as `$(...)`).
// Its stderr goes to Stderr.
func (r *Repo) gitOut(ctx context.Context, args ...string) (string, error) {
	out, err := r.Runner.Capture(ctx, r.gitCmd(args, nil, r.Stderr))
	return strings.TrimRight(string(out), "\n"), err
}

// gitOutQuiet is gitOut with git's stderr dropped.
func (r *Repo) gitOutQuiet(ctx context.Context, args ...string) (string, error) {
	out, err := r.Runner.Capture(ctx, r.gitCmd(args, nil, io.Discard))
	return strings.TrimRight(string(out), "\n"), err
}

// gitRaw captures git's stdout as it is, for -z output.
func (r *Repo) gitRaw(ctx context.Context, args ...string) (string, error) {
	out, err := r.Runner.Capture(ctx, r.gitCmd(args, nil, r.Stderr))
	return string(out), err
}

// splitZ splits NUL-separated git output.
func splitZ(s string) []string {
	s = strings.TrimSuffix(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

func (r *Repo) hasRemote(ctx context.Context, name string) bool {
	return r.gitQuiet(ctx, "remote", "get-url", name) == nil
}

// configBool reports whether `git config --bool key` says true.
func (r *Repo) configBool(ctx context.Context, key string) bool {
	out, err := r.gitOutQuiet(ctx, "config", "--bool", key)
	return err == nil && out == "true"
}

// unsetConfig removes a key; a key that is not set is not an error.
func (r *Repo) unsetConfig(ctx context.Context, key string) {
	_ = r.gitQuiet(ctx, "config", "--unset", key)
}

// headCommit prints HEAD's commit, or "" on an unborn branch.
func (r *Repo) headCommit(ctx context.Context) string {
	out, err := r.gitOutQuiet(ctx, "rev-parse", "-q", "--verify", "HEAD")
	if err != nil {
		return ""
	}
	return out
}
