package terraform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

type fixedClock int64

func (c fixedClock) Unix() int64 { return int64(c) }

func TestPlanApplyCreatesPlanPromptsAndAppliesAcceptedPlan(t *testing.T) {
	withProject(t, func() {
		writeFile(t, ".terraform-version", "1.7.3\n")
		writeFile(t, "vars/common/common.tfvars", "")
		writeFile(t, "vars/dev/dev.tfvars", "")
		fake := &runner.Fake{CaptureOut: [][]byte{[]byte("default\n"), []byte("dev\n")}}
		rt := &app.Runtime{
			Target:     "dev",
			Runner:     fake,
			Stdin:      strings.NewReader("yes\n"),
			Stdout:     &strings.Builder{},
			Stderr:     &strings.Builder{},
			Clock:      fixedClock(123),
			SkipPrompt: true,
		}

		err := (&PlanCmd{ApplyPlan: true}).Run(context.Background(), rt)
		if err != nil {
			t.Fatalf("plan --apply failed: %v\nrecords:\n%s", err, fake.Output())
		}

		commands := fake.Commands()
		if len(commands) != 8 {
			t.Fatalf("command count = %d, want 8\nrecords:\n%s", len(commands), fake.Output())
		}
		planCmd := commands[3]
		if len(planCmd) != 5 || planCmd[0] != "terraform" || planCmd[1] != "plan" {
			t.Fatalf("plan command = %#v", planCmd)
		}
		planPath := strings.TrimPrefix(planCmd[4], "-out=")
		if !strings.HasPrefix(planPath, filepath.Join(".tf-plans", "tf-plan-dev-123-")) || !strings.HasSuffix(planPath, ".out") {
			t.Fatalf("plan path = %q, want generated .tf-plans/tf-plan-dev-123-*.out", planPath)
		}
		wantPrefix := [][]string{
			{"flock", tfenvInstallLockPath, "tfenv", "install"},
			{"terraform", "workspace", "show"},
			{"terraform", "workspace", "select", "dev"},
			{"terraform", "plan", "-var-file=vars/common/common.tfvars", "-var-file=vars/dev/dev.tfvars", planCmd[4]},
			{"flock", tfenvInstallLockPath, "tfenv", "install"},
			{"terraform", "workspace", "show"},
			{"terraform", "workspace", "select", "dev"},
			{"terraform", "apply", planPath},
		}
		if !reflect.DeepEqual(commands, wantPrefix) {
			t.Fatalf("commands = %#v, want %#v\nrecords:\n%s", commands, wantPrefix, fake.Output())
		}
	})
}

func TestWorkspacePromptRejectsDefaultNo(t *testing.T) {
	withProject(t, func() {
		writeFile(t, ".terraform-version", "1.7.3\n")
		writeFile(t, "vars/dev/dev.tfvars", "")
		fake := &runner.Fake{CaptureOut: [][]byte{[]byte("prod\n")}}
		rt := &app.Runtime{
			Target: "dev",
			Runner: fake,
			Stdin:  strings.NewReader("\n"),
			Stdout: &strings.Builder{},
			Stderr: &strings.Builder{},
		}

		err := (&ApplyCmd{Approve: true}).Run(context.Background(), rt)
		var exitErr *runner.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 1 {
			t.Fatalf("error = %v, want exit code 1", err)
		}
		want := [][]string{
			{"flock", tfenvInstallLockPath, "tfenv", "install"},
			{"terraform", "workspace", "show"},
		}
		if got := fake.Commands(); !reflect.DeepEqual(got, want) {
			t.Fatalf("commands = %#v, want prompt to stop before apply\nrecords:\n%s", got, fake.Output())
		}
	})
}

func TestTfenvInitUsesFlockWhenTerraformVersionExists(t *testing.T) {
	withProject(t, func() {
		writeFile(t, ".terraform-version", "1.7.5\n")
		fake := &runner.Fake{}
		s := &service{
			runner: fake,
			stderr: &strings.Builder{},
		}

		if err := s.tfenvInit(context.Background()); err != nil {
			t.Fatalf("tfenvInit failed: %v", err)
		}

		want := [][]string{{"flock", tfenvInstallLockPath, "tfenv", "install"}}
		if got := fake.Commands(); !reflect.DeepEqual(got, want) {
			t.Fatalf("commands = %#v, want locked tfenv install\nrecords:\n%s", got, fake.Output())
		}
	})
}

func TestTfenvInitSkipsInstallWhenTerraformVersionMissing(t *testing.T) {
	withProject(t, func() {
		fake := &runner.Fake{}
		stderr := &strings.Builder{}
		s := &service{
			runner: fake,
			stderr: stderr,
		}

		if err := s.tfenvInit(context.Background()); err != nil {
			t.Fatalf("tfenvInit failed: %v", err)
		}
		if got := fake.Commands(); len(got) != 0 {
			t.Fatalf("commands = %#v, want no tfenv install\nrecords:\n%s", got, fake.Output())
		}
		if !strings.Contains(stderr.String(), ".terraform-version not found") {
			t.Fatalf("stderr = %q, want missing .terraform-version warning", stderr.String())
		}
	})
}

func withProject(t *testing.T, fn func()) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	})
	fn()
}

func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
