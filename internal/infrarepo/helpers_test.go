package infrarepo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/luthersystems/mars/internal/runner"
)

// These tests run real git on temp repos: a bare repo stands for the
// per-project -infra repo on GitHub (a file:// URL), and a project tree is
// a directory laid out like a sandbox-infrastructure-template project. They
// are the Go port of the template's tests/test-infra-push.sh; each test
// names the shell case (A, B, ...) it covers.

func TestMain(m *testing.M) {
	// No user or system git config: the tests see only what they set.
	home, err := os.MkdirTemp("", "infrarepo-home-")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"MARS_PROJECT_ROOT":   "",
	} {
		_ = os.Setenv(k, v)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// git runs git in dir and fails the test on error. It returns stdout,
// trimmed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// gitErr runs git in dir with a test identity.
func gitErr(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.email=t@l", "-c", "user.name=T", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...) //nolint:gosec // G204: the test's own git fixtures
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(stdout.String() + stderr.String()), err
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile keeps an existing file's mode
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

// newBare makes an empty bare repo; it returns its path.
func newBare(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "infra.git")
	git(t, ".", "init", "-q", "--bare", "-b", "main", bare)
	return bare
}

func fileURL(path string) string { return "file://" + path }

// newProject makes a project tree as the template's make_project did: the
// template's real .gitignore, auto-vars with no repo URL, a decoy outputs
// file and a few tracked-looking files.
func newProject(t *testing.T) string {
	t.Helper()
	proj := filepath.Join(t.TempDir(), "proj")
	writeFile(t, filepath.Join(proj, ".gitignore"), readTestdata(t, "template.gitignore"), 0o644)
	writeFile(t, filepath.Join(proj, "ansible/inventories/default/group_vars/all/env.yaml"), "environment: test\n", 0o644)
	writeFile(t, filepath.Join(proj, "tf/auto-vars/common.auto.tfvars.json"), `{"cloud_provider": "aws"}`+"\n", 0o644)
	// A decoy deploy-time output: it must never ride the -infra push (#160).
	writeFile(t, filepath.Join(proj, "outputs/dummy.json"), `{"dummy": true}`+"\n", 0o644)
	writeFile(t, filepath.Join(proj, "shell_utils.sh"), "#!/usr/bin/env bash\n# the template's helpers\n", 0o644)
	writeFile(t, filepath.Join(proj, "tf/utils.sh"), "# shellcheck shell=bash\n", 0o644)
	return proj
}

// setAutoVars writes tf/auto-vars/common.auto.tfvars.json.
func setAutoVars(t *testing.T, proj string, vars map[string]any) {
	t.Helper()
	b, err := json.Marshal(vars)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, "tf/auto-vars/common.auto.tfvars.json"), string(b)+"\n", 0o644)
}

func setRepoURL(t *testing.T, proj, url string) {
	t.Helper()
	setAutoVars(t, proj, map[string]any{"cloud_provider": "aws", repoURLVar: url})
}

// initGit makes the project root a git repo on branch main.
func initGit(t *testing.T, proj string) {
	t.Helper()
	git(t, proj, "init", "-q", "-b", "main")
	git(t, proj, "config", "user.email", "test@luthersystems.com")
	git(t, proj, "config", "user.name", "Test")
}

// commitAll commits everything in dir.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
}

// seedInfra pushes a one-commit main with infra.txt to bare; it returns the
// commit.
func seedInfra(t *testing.T, bare string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "seed")
	git(t, ".", "init", "-q", "-b", "main", src)
	writeFile(t, filepath.Join(src, "infra.txt"), "infra\n", 0o644)
	commitAll(t, src, "infra base")
	git(t, src, "push", "-q", fileURL(bare), "main")
	return git(t, src, "rev-parse", "HEAD")
}

// mateCommit clones bare, adds a file and pushes, as a teammate would.
func mateCommit(t *testing.T, bare, name, body string) string {
	t.Helper()
	mate := filepath.Join(t.TempDir(), "mate")
	git(t, ".", "clone", "-q", "-b", "main", fileURL(bare), mate)
	writeFile(t, filepath.Join(mate, name), body, 0o644)
	commitAll(t, mate, "mate edit")
	git(t, mate, "push", "-q", "origin", "main")
	return git(t, mate, "rev-parse", "HEAD")
}

func commitCount(t *testing.T, bare string) int {
	t.Helper()
	out, err := gitErr(bare, "rev-list", "--all", "--count")
	if err != nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscan(out, &n)
	return n
}

func tree(t *testing.T, repo, rev string) []string {
	t.Helper()
	out, err := gitErr(repo, "ls-tree", "-r", "--name-only", rev)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func hasPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

func mode(t *testing.T, repo, rev, path string) string {
	t.Helper()
	out := git(t, repo, "ls-tree", rev, path)
	m, _, _ := strings.Cut(out, " ")
	return m
}

// configValue reads key from the repo's own config (gitErr's -c identity
// would shadow it).
func configValue(dir, key string) string {
	out, _ := exec.Command("git", "-C", dir, "config", "--local", key).Output() //nolint:gosec // G204: as in gitErr
	return strings.TrimRight(string(out), "\n")
}

func head(dir string) string {
	out, _ := gitErr(dir, "rev-parse", "-q", "--verify", "HEAD")
	return out
}

// syncBuffer is an output both the Repo and the git processes write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// shimRunner fails the git commands fail matches with code, and runs the
// rest: the Go form of the shell tests' PATH shims.
type shimRunner struct {
	runner.Runner
	fail func(argv []string) bool
	code int
}

func (s shimRunner) Run(ctx context.Context, cmd runner.Command) error {
	if s.fail != nil && s.fail(cmd.Argv) {
		return &runner.ExitError{Code: s.code}
	}
	return s.Runner.Run(ctx, cmd)
}

func (s shimRunner) Capture(ctx context.Context, cmd runner.Command) ([]byte, error) {
	if s.fail != nil && s.fail(cmd.Argv) {
		return nil, &runner.ExitError{Code: s.code}
	}
	return s.Runner.Capture(ctx, cmd)
}

// gitArgs drops the leading "git" and its global options (-c k=v,
// --literal-pathspecs), so a[0] is the subcommand.
func gitArgs(argv []string) []string {
	if len(argv) == 0 || argv[0] != "git" {
		return nil
	}
	a := argv[1:]
	for len(a) > 0 && strings.HasPrefix(a[0], "-") {
		if a[0] == "-c" && len(a) >= 2 {
			a = a[1:]
		}
		a = a[1:]
	}
	return a
}

func has(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

// failGit fails git <sub> when every extra flag is present.
func failGit(sub string, flags ...string) func([]string) bool {
	return func(argv []string) bool {
		a := gitArgs(argv)
		if len(a) == 0 || a[0] != sub {
			return false
		}
		for _, f := range flags {
			if !has(a, f) {
				return false
			}
		}
		return true
	}
}

type repoOpt func(*Repo)

func withEnv(env map[string]string) repoOpt {
	return func(r *Repo) { r.Getenv = func(k string) string { return env[k] } }
}

func withFail(code int, fail func([]string) bool) repoOpt {
	return func(r *Repo) { r.Runner = shimRunner{Runner: r.Runner, fail: fail, code: code} }
}

// newRepo is a Repo on root with real git; its output (stdout and stderr,
// in order) is the returned buffer. No token file unless withEnv gives one.
func newRepo(root string, opts ...repoOpt) (*Repo, *syncBuffer) {
	out := &syncBuffer{}
	r := &Repo{
		Root:   root,
		Runner: &runner.Real{Stdout: out, Stderr: out},
		Stdout: out,
		Stderr: out,
		Getenv: func(string) string { return "" },
	}
	for _, o := range opts {
		o(r)
	}
	return r, out
}

func ctx() context.Context { return context.Background() }

func assertContains(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}
