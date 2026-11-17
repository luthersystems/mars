package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/runner"
)

// The stage commands (plan-all, plan-stage, apply-stage) are opt-in: no
// other command reads mars-stages.yaml, and none behaves differently when
// one exists. TestOldCommandsMatchMain runs the older commands through the
// fake runner, without a config, with a valid one and with one that is not
// YAML, and compares every command line, its directory, environment and
// stdin, mars's stdout and stderr and the exit code with
// testdata/optin.golden. That golden was written by this same test run
// against origin/main (before the stage commands), so it is main's
// behaviour; regenerate it there, never here:
//
//	git worktree add /tmp/mars-main origin/main
//	cp internal/cli/optin_test.go /tmp/mars-main/internal/cli/
//	(cd /tmp/mars-main && go test ./internal/cli -run TestOldCommandsMatchMain -update-optin)
//	cp /tmp/mars-main/internal/cli/testdata/optin.golden internal/cli/testdata/
var updateOptin = flag.Bool("update-optin", false, "write testdata/optin.golden")

type optinCase struct {
	name    string
	args    []string
	stdin   string
	capture []string // the fake runner's Capture outputs, in order
	runErrs []error  // the fake runner's Run errors, in order
}

func optinCases() []optinCase {
	plan := `{"format_version":"1.2","resource_changes":[]}`
	return []optinCase{
		{name: "init", args: []string{"dev", "init"}, capture: []string{"dev\n"}},
		{name: "init flags", args: []string{"dev", "--skip-prompt", "init", "--upgrade", "--reconfigure", "--backend-config", "b.hcl"}, capture: []string{"default\n"}},
		{name: "init init fails", args: []string{"dev", "init"}, capture: []string{"dev\n"}, runErrs: []error{nil, &runner.ExitError{Code: 3}}},
		{name: "plan skip-prompt", args: []string{"dev", "--skip-prompt", "plan"}, capture: []string{"default\n"}},
		{name: "plan same workspace", args: []string{"dev", "plan", "--out", "p.out"}, capture: []string{"dev\n"}},
		{name: "plan prompt yes", args: []string{"dev", "plan"}, stdin: "y\n", capture: []string{"default\n"}},
		{name: "plan prompt no", args: []string{"dev", "plan"}, stdin: "\n", capture: []string{"default\n"}},
		{name: "plan prompt eof", args: []string{"dev", "plan"}, capture: []string{"default\n"}},
		{name: "plan prompt retry", args: []string{"dev", "plan"}, stdin: "maybe\nyes\n", capture: []string{"default\n"}},
		{name: "plan fails", args: []string{"dev", "plan"}, capture: []string{"dev\n"}, runErrs: []error{nil, nil, nil, &runner.ExitError{Code: 1}}},
		{name: "plan apply yes", args: []string{"dev", "--skip-prompt", "plan", "--apply"}, stdin: "yes\n", capture: []string{"dev\n", "dev\n"}},
		{name: "plan apply eof", args: []string{"dev", "--skip-prompt", "plan", "--apply"}, capture: []string{"dev\n", "dev\n"}},
		{name: "plan apply retry", args: []string{"dev", "--skip-prompt", "plan", "--apply"}, stdin: "no\nyes\n", capture: []string{"dev\n", "dev\n"}},
		// Both prompts answered on one pipe: main's first prompt reads with
		// its own buffered reader, so the confirm finds stdin drained.
		{name: "plan apply piped answers", args: []string{"dev", "plan", "--apply"}, stdin: "y\nyes\n", capture: []string{"default\n", "default\n"}},
		{name: "apply", args: []string{"dev", "apply"}, stdin: "yes\n", capture: []string{"dev\n", "dev\n"}},
		{name: "apply eof", args: []string{"dev", "apply"}, capture: []string{"dev\n", "dev\n"}},
		{name: "apply approve", args: []string{"dev", "apply", "--approve"}, capture: []string{"dev\n"}},
		{name: "apply approve targets", args: []string{"dev", "apply", "--approve", "--target", "module.a", "--target", "module.b"}, capture: []string{"dev\n"}},
		{name: "apply plan file", args: []string{"dev", "apply", "--plan", "saved.out"}, capture: []string{"dev\n"}},
		{name: "apply forbid", args: []string{"dev", "apply", "--approve", "--forbid-resource-changes"}, capture: []string{"dev\n", plan}},
		{name: "apply prompt no", args: []string{"dev", "apply", "--approve"}, stdin: "n\n", capture: []string{"prod\n"}},
		{name: "destroy", args: []string{"dev", "destroy"}, capture: []string{"dev\n"}},
		{name: "destroy approve", args: []string{"dev", "destroy", "--approve", "--target", "module.a"}, capture: []string{"dev\n"}},
		{name: "terraform output", args: []string{"dev", "terraform", "output", "name"}, capture: []string{"dev\n"}},
		{name: "terraform output json", args: []string{"dev", "--skip-prompt", "terraform", "--", "output", "-json"}, capture: []string{"default\n"}},
		{name: "terraform fails", args: []string{"dev", "terraform", "state", "list"}, capture: []string{"dev\n"}, runErrs: []error{nil, nil, &runner.ExitError{Code: 4}}},
		{name: "show", args: []string{"dev", "show"}, capture: []string{"dev\n"}},
		{name: "new-workspace", args: []string{"dev", "new-workspace"}},
		{name: "no env vars", args: []string{"qa", "plan"}},
	}
}

var optinConfigs = []struct{ name, body string }{
	{"no config", ""},
	{"valid config", "version: 1\nstages:\n  - name: a\n    dir: .\n"},
	{"invalid config", "this: [is not\n  : valid yaml\n"},
}

func TestOldCommandsMatchMain(t *testing.T) {
	golden := filepath.Join("testdata", "optin.golden")
	var want []byte
	if !*updateOptin {
		var err error
		if want, err = os.ReadFile(golden); err != nil {
			t.Fatal(err)
		}
	}
	for _, cfg := range optinConfigs {
		var got bytes.Buffer
		for _, c := range optinCases() {
			fmt.Fprintf(&got, "=== %s\n", c.name)
			// A subtest each, so t.Chdir's directory ends with the case.
			t.Run(cfg.name+"/"+c.name, func(t *testing.T) {
				got.WriteString(runOptinCase(t, c, cfg.body))
			})
		}
		if *updateOptin {
			if cfg.body != "" {
				continue // only the run with no config writes the golden
			}
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: old commands differ from main's (testdata/optin.golden)\n%s", cfg.name, firstDiff(want, got.Bytes()))
		}
	}
}

// optinVolatile are the parts of a run that change between runs: the
// project directory and the generated plan files' time and random suffix.
var optinVolatile = regexp.MustCompile(`(tf-plan(-guard)?-dev)-[0-9]+-[A-Za-z0-9]+`)

func runOptinCase(t *testing.T, c optinCase, config string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	files := map[string]string{
		".terraform-version":        "1.7.3\n",
		"vars/common/common.tfvars": "",
		"vars/dev/dev.tfvars":       "",
	}
	if config != "" {
		files["mars-stages.yaml"] = config
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake := &runner.Fake{RunErrors: append([]error(nil), c.runErrs...)}
	for _, out := range c.capture {
		fake.CaptureOut = append(fake.CaptureOut, []byte(out))
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), c.args, strings.NewReader(c.stdin), &stdout, &stderr, fake)

	var b strings.Builder
	fmt.Fprintf(&b, "exit %d\n", code)
	for _, r := range fake.Records {
		fmt.Fprintf(&b, "%s %s\n", r.Kind, runner.ShellCommand(r.Cmd.Argv))
		if r.Cmd.Dir != "" {
			fmt.Fprintf(&b, "  dir %s\n", r.Cmd.Dir)
		}
		keys := make([]string, 0, len(r.Cmd.Env))
		for k := range r.Cmd.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  env %s=%s\n", k, r.Cmd.Env[k])
		}
		if r.Cmd.Stdin != nil {
			b.WriteString("  stdin set\n")
		}
		if r.Cmd.Quiet {
			b.WriteString("  quiet\n")
		}
	}
	fmt.Fprintf(&b, "stdout:\n%s\nstderr:\n%s\n", stdout.String(), stderr.String())
	out := strings.ReplaceAll(b.String(), dir, "$DIR")
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		out = strings.ReplaceAll(out, real, "$DIR")
	}
	return optinVolatile.ReplaceAllString(out, "${1}-TIME-RAND")
}

// firstDiff shows the first line where got differs from want.
func firstDiff(want, got []byte) string {
	w := strings.Split(string(want), "\n")
	g := strings.Split(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  main: %q\n  now:  %q", i+1, wl, gl)
		}
	}
	return "(lengths differ)"
}

// The stage commands need the config: without one they fail with exit 1
// and an error that names the file, and run nothing.
func TestStageCommandsNeedConfig(t *testing.T) {
	for _, args := range [][]string{
		{"dev", "plan-all"},
		{"dev", "plan-stage", "a"},
		{"dev", "apply-stage", "a"},
	} {
		t.Run(args[1], func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			fake := &runner.Fake{}
			var stdout, stderr bytes.Buffer
			code := Main(context.Background(), args, strings.NewReader(""), &stdout, &stderr, fake)
			if code != 1 {
				t.Fatalf("exit %d, want 1; stderr:\n%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "mars-stages.yaml") {
				t.Fatalf("stderr does not name the config:\n%s", stderr.String())
			}
			if len(fake.Records) != 0 {
				t.Fatalf("ran commands without a config:\n%s", fake.Output())
			}
		})
	}
}
