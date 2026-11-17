package terraform_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/cli"
	"github.com/luthersystems/mars/internal/runner"
)

// The differential test runs sandbox-infrastructure-template tf/drift-check.sh
// (copied verbatim into testdata/driftcheck from template commit
// 412aeb3d9f6e27a4137b1d148281f263070897a2) and `mars <env> drift-check` on
// the same plan fixtures, with the same fake terraform, and requires the same
// exit code, stdout, stderr and outputs/ files byte for byte. The fixtures
// under testdata/driftcheck/plans are the template's tests/test-drift-check.sh
// plans plus edge cases (number literals, escapes, malformed plans).
//
// Recopy drift-check.sh when the template changes it; this test then shows
// what the port must follow.

type runResult struct {
	code   int
	stdout string
	stderr string
	files  map[string]string
}

// driftTestdataDir is made absolute at package init, before any test
// changes directory.
var driftTestdataDir = func() string {
	dir, err := filepath.Abs("testdata/driftcheck")
	if err != nil {
		panic(err)
	}
	return dir
}()

func driftTestdata(*testing.T) string { return driftTestdataDir }

// setupDriftEnv makes a work dir with a plan file and a bin dir holding the
// fake terraform, puts the bin dir first on PATH and points the fake at the
// fixture. It returns the work dir and the project root.
func setupDriftEnv(t *testing.T, fixture string) (string, string) {
	t.Helper()
	td := driftTestdata(t)
	work := t.TempDir()
	bin := filepath.Join(work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(filepath.Join(td, "fake-terraform"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "terraform"), fake, 0o755); err != nil { //nolint:gosec // G306: the fake must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "test.tfplan"), []byte("fake-plan-binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if fixture != "" {
		t.Setenv("FAKE_SHOW_OUTPUT", filepath.Join(td, "plans", fixture))
	}
	root := filepath.Join(work, "project")
	t.Setenv("MARS_PROJECT_ROOT", root)
	t.Chdir(work)
	return work, root
}

func snapshotOutputs(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	outputs := filepath.Join(root, "outputs")
	_ = filepath.Walk(outputs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		rel, _ := filepath.Rel(outputs, p)
		files[rel] = string(b)
		return nil
	})
	if err := os.RemoveAll(outputs); err != nil {
		t.Fatal(err)
	}
	return files
}

func runShellDriftCheck(t *testing.T, root string, args []string) runResult {
	t.Helper()
	script := filepath.Join(driftTestdata(t), "drift-check.sh")
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return runResult{code: code, stdout: out.String(), stderr: errb.String(), files: snapshotOutputs(t, root)}
}

func runMarsDriftCheck(t *testing.T, root string, args []string) runResult {
	t.Helper()
	var out, errb bytes.Buffer
	r := &runner.Real{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}
	argv := append([]string{"default", "drift-check"}, args...)
	code := cli.Main(context.Background(), argv, strings.NewReader(""), &out, &errb, r)
	return runResult{code: code, stdout: out.String(), stderr: errb.String(), files: snapshotOutputs(t, root)}
}

func requireShellTools(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH; the differential test needs it", tool)
		}
	}
	v, err := exec.Command("jq", "--version").Output()
	if err != nil {
		t.Skip("jq --version failed")
	}
	return strings.TrimSpace(string(v))
}

// stderrMode says how far the port matches the script's stderr for a fixture.
type stderrMode int

const (
	stderrExact stderrMode = iota
	// jq's runtime error texts: the port prints jq 1.7's (the mars image
	// has jq 1.7.1, the same texts); another jq words them differently.
	stderrJQ17
	// jq's JSON parse error text is not reproduced; only the exit code is.
	stderrParse
)

var driftFixtures = map[string]stderrMode{
	"action_create_plan.json":        stderrExact,
	"action_delete_plan.json":        stderrExact,
	"action_missing_plan.json":       stderrExact,
	"action_replace_plan.json":       stderrExact,
	"action_update_plan.json":        stderrExact,
	"actionable_plan.json":           stderrExact,
	"array_before_plan.json":         stderrExact,
	"drift_plan.json":                stderrExact,
	"empty_plan.json":                stderrExact,
	"enrich_plan.json":               stderrExact,
	"false_positive_plan.json":       stderrExact,
	"issue_209_plan.json":            stderrExact,
	"mixed_actions_plan.json":        stderrExact,
	"mixed_drift_plan.json":          stderrExact,
	"mixed_plan.json":                stderrExact,
	"nested_plan.json":               stderrExact,
	"noop_drift_plan.json":           stderrExact,
	"noop_plan.json":                 stderrExact,
	"null_change_plan.json":          stderrExact,
	"numbers_plan.json":              stderrExact,
	"object_iter_plan.json":          stderrExact,
	"phantom_plan.json":              stderrExact,
	"provenance_plan.json":           stderrExact,
	"read_plan.json":                 stderrExact,
	"real_drift_plan.json":           stderrExact,
	"refresh_only_plan.json":         stderrExact,
	"replace_plan.json":              stderrExact,
	"strings_plan.json":              stderrExact,
	"whitespace_plan.json":           stderrExact,
	"array_address_plan.json":        stderrJQ17,
	"before_string_plan.json":        stderrJQ17,
	"change_string_plan.json":        stderrJQ17,
	"drift_address_number_plan.json": stderrJQ17,
	"drift_string_plan.json":         stderrJQ17,
	"rc_address_number_plan.json":    stderrJQ17,
	"top_array_plan.json":            stderrJQ17,
	"invalid_plan.json":              stderrParse,
}

func TestDriftFixturesAllListed(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(driftTestdata(t), "plans"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := driftFixtures[e.Name()]; !ok {
			t.Errorf("fixture %s is not in driftFixtures", e.Name())
		}
	}
}

func compareRuns(t *testing.T, sh, gor runResult, mode stderrMode, jqVersion string) {
	t.Helper()
	if sh.code != gor.code {
		t.Errorf("exit code: shell %d, mars %d\nshell stderr: %s\nmars stderr: %s", sh.code, gor.code, sh.stderr, gor.stderr)
	}
	if sh.stdout != gor.stdout {
		t.Errorf("stdout differs\n--- shell\n%s\n--- mars\n%s", sh.stdout, gor.stdout)
	}
	checkStderr := mode == stderrExact ||
		(mode == stderrJQ17 && (jqVersion == "jq-1.7" || jqVersion == "jq-1.7.1"))
	if checkStderr && sh.stderr != gor.stderr {
		t.Errorf("stderr differs\n--- shell\n%s\n--- mars\n%s", sh.stderr, gor.stderr)
	}
	names := func(m map[string]string) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if strings.Join(names(sh.files), ",") != strings.Join(names(gor.files), ",") {
		t.Errorf("outputs files: shell %v, mars %v", names(sh.files), names(gor.files))
	}
	for name, body := range sh.files {
		if gor.files[name] != body {
			t.Errorf("outputs/%s differs\n--- shell\n%s\n--- mars\n%s", name, body, gor.files[name])
		}
	}
}

func TestDriftCheckMatchesShellScript(t *testing.T) {
	jqVersion := requireShellTools(t)
	flagSets := [][]string{
		nil,
		{"--ignore-drift"},
		{"--strict"},
		{"--stage", "cloud-provision"},
		{"--stage", "mystack", "--ignore-drift"},
		{"--strict", "--ignore-drift"},
		{"--stage", "s", "--strict"},
	}
	fixtures := make([]string, 0, len(driftFixtures))
	for f := range driftFixtures {
		fixtures = append(fixtures, f)
	}
	sort.Strings(fixtures)
	for _, fixture := range fixtures {
		for _, flags := range flagSets {
			for _, versions := range []bool{false, true} {
				if versions && flags != nil {
					// The provenance fields do not interact with the flags.
					continue
				}
				name := fixture + "/" + strings.Join(flags, "_")
				if versions {
					name += "/versions"
				}
				t.Run(name, func(t *testing.T) {
					_, root := setupDriftEnv(t, fixture)
					if versions {
						t.Setenv("TEMPLATE_VERSION", "v1.2.3")
						t.Setenv("PRESETS_VERSION", "v1.4.2")
					} else {
						t.Setenv("TEMPLATE_VERSION", "")
						t.Setenv("PRESETS_VERSION", "")
					}
					args := append([]string{"test.tfplan"}, flags...)
					sh := runShellDriftCheck(t, root, args)
					gor := runMarsDriftCheck(t, root, args)
					compareRuns(t, sh, gor, driftFixtures[fixture], jqVersion)
				})
			}
		}
	}
}

// TestDriftCheckErrorPathsMatchShellScript covers the paths that do not
// depend on the plan: a missing plan file, a failing terraform show (its
// exit code and stderr pass through) and an outputs dir that cannot be made.
func TestDriftCheckErrorPathsMatchShellScript(t *testing.T) {
	jqVersion := requireShellTools(t)
	cases := []struct {
		name  string
		setup func(t *testing.T, work, root string)
		args  []string
	}{
		{name: "missing plan file", args: []string{"nope.tfplan"}},
		{name: "plan path is a directory", args: []string{"bin"}},
		{name: "terraform show fails", args: []string{"test.tfplan"}, setup: func(t *testing.T, _, _ string) {
			t.Setenv("FAKE_SHOW_RC", "3")
			t.Setenv("FAKE_SHOW_STDERR", "Error: Failed to read the given file as a state or plan file")
		}},
		{name: "terraform show fails with output", args: []string{"test.tfplan", "--strict"}, setup: func(t *testing.T, _, _ string) {
			t.Setenv("FAKE_SHOW_RC", "1")
		}},
		{name: "outputs dir is a file", args: []string{"test.tfplan"}, setup: func(t *testing.T, _, root string) {
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "outputs"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			work, root := setupDriftEnv(t, "drift_plan.json")
			t.Setenv("TEMPLATE_VERSION", "")
			t.Setenv("PRESETS_VERSION", "")
			if c.setup != nil {
				c.setup(t, work, root)
			}
			sh := runShellDriftCheck(t, root, c.args)
			// A failed run must not leave the outputs file the case made.
			if c.name == "outputs dir is a file" {
				if err := os.WriteFile(filepath.Join(root, "outputs"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gor := runMarsDriftCheck(t, root, c.args)
			mode := stderrExact
			if c.name == "outputs dir is a file" {
				// mkdir's own text is reproduced, bash's line prefix is not.
				mode = stderrParse
			}
			compareRuns(t, sh, gor, mode, jqVersion)
		})
	}
}
