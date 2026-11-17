package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/runner"
)

// The infra-* commands are wired with their exit codes, read the project
// root from --root or $MARS_PROJECT_ROOT, and print no "exit status" line
// of mars's own (the template parses their output and exit codes).
func TestInfraCommandsExitCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitT := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@l", "-c", "user.name=T"}, args...)...) //nolint:gosec // G204: the test's own git fixtures
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bare := filepath.Join(t.TempDir(), "infra.git")
	gitT(".", "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(t.TempDir(), "seed")
	gitT(".", "init", "-q", "-b", "main", seed)
	gitT(seed, "commit", "-q", "--allow-empty", "-m", "infra")
	gitT(seed, "push", "-q", "file://"+bare, "main")
	proj := t.TempDir()
	gitT(proj, "init", "-q", "-b", "main")
	gitT(proj, "remote", "add", "infra", "file://"+bare)

	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		rc := Main(context.Background(), args, strings.NewReader(""), &out, &out, &runner.Real{Stdout: &out, Stderr: &out})
		return rc, out.String()
	}
	// An unborn HEAD: not merged, rc 2.
	if rc, out := run("default", "infra-merge", "--root", proj); rc != 2 || strings.Contains(out, "exit status") {
		t.Errorf("infra-merge on an unborn HEAD: rc=%d\n%s", rc, out)
	}
	t.Setenv("MARS_PROJECT_ROOT", proj)
	if rc, out := run("default", "infra-merge", "--abort-on-conflict"); rc != 2 {
		t.Errorf("infra-merge with $MARS_PROJECT_ROOT: rc=%d\n%s", rc, out)
	}
	// A snapshot on top of infra/main, then a push that fast-forwards it.
	if rc, out := run("default", "infra-snapshot", "snap"); rc != 0 {
		t.Errorf("infra-snapshot: rc=%d\n%s", rc, out)
	}
	if rc, out := run("default", "infra-push"); rc != 0 || !strings.Contains(out, "Pushed to infra remote") {
		t.Errorf("infra-push: rc=%d\n%s", rc, out)
	}
	// A refused commit (rebase marker) exits 1.
	gitT(proj, "config", "core.fileMode", "false")
	if err := os.Mkdir(filepath.Join(proj, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rc, out := run("default", "infra-commit"); rc != 1 || !strings.Contains(out, "git rebase --abort") {
		t.Errorf("infra-commit during a rebase: rc=%d\n%s", rc, out)
	}
	// infra-pull refuses the same rebase: the merge exits 1.
	if rc, out := run("default", "infra-pull"); rc != 1 {
		t.Errorf("infra-pull during a rebase: rc=%d\n%s", rc, out)
	}
	// The never-failing commands exit 0 even when they only warn.
	for _, cmd := range []string{"infra-clone", "infra-persist", "infra-push"} {
		if rc, out := run("default", cmd); rc != 0 {
			t.Errorf("%s: rc=%d\n%s", cmd, rc, out)
		}
	}
}
