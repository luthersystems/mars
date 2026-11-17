package terraform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/cli"
	"github.com/luthersystems/mars/internal/runner"
)

// The differential tests run the template's shell and the mars commands on
// the same plan fixtures and compare the parsed JSON fields they share
// (field parity), not bytes: mars writes its own JSON with encoding/json.
//
//   - drift-check: testdata/driftcheck/drift-check.sh is
//     sandbox-infrastructure-template tf/drift-check.sh, copied verbatim from
//     template commit 412aeb3d9f6e27a4137b1d148281f263070897a2. The fields
//     compared are those of its drift.json except the template's
//     template_version and presets_version.
//   - plan-summary: testdata/plansummary/counts.sh is the per-stage count
//     filter of tf/plan-all.sh, copied verbatim. The fields compared are
//     add, change, destroy, import and has_changes.
//
// The fixtures under testdata/driftcheck/plans are the template's
// tests/test-drift-check.sh plans plus edge cases; testdata/plansummary/plans
// add the plan-summary cases. Recopy the scripts when the template changes
// them; these tests then show what mars must follow.

// driftTestdataDir is made absolute at package init, before any test
// changes directory.
var driftTestdataDir = func() string {
	dir, err := filepath.Abs("testdata")
	if err != nil {
		panic(err)
	}
	return dir
}()

// setupShellEnv makes a work dir with a binary-looking plan file and a bin
// dir holding the fake terraform, puts the bin dir first on PATH and points
// the fake at the fixture. It returns the project root.
func setupShellEnv(t *testing.T, fixture string) string {
	t.Helper()
	work := t.TempDir()
	bin := filepath.Join(work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(filepath.Join(driftTestdataDir, "driftcheck", "fake-terraform"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "terraform"), fake, 0o755); err != nil { //nolint:gosec // G306: the fake must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "test.tfplan"), []byte("PK fake-plan-binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SHOW_OUTPUT", fixture)
	root := filepath.Join(work, "project")
	t.Setenv("MARS_PROJECT_ROOT", root)
	t.Setenv("TEMPLATE_VERSION", "")
	t.Setenv("PRESETS_VERSION", "")
	t.Chdir(work)
	return root
}

type cmdResult struct {
	code   int
	stdout string
	stderr string
}

func runShell(t *testing.T, script string, args ...string) cmdResult {
	t.Helper()
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
	return cmdResult{code: code, stdout: out.String(), stderr: errb.String()}
}

func runMars(t *testing.T, args ...string) cmdResult {
	t.Helper()
	var out, errb bytes.Buffer
	r := &runner.Real{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}
	code := cli.Main(context.Background(), append([]string{"default"}, args...), strings.NewReader(""), &out, &errb, r)
	return cmdResult{code: code, stdout: out.String(), stderr: errb.String()}
}

func requireShellTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH; the differential test needs it", tool)
		}
	}
}

// parseFields decodes a JSON object into generic values (numbers as
// float64, so 1e2 and 100 compare equal) and keeps only the named fields.
func parseFields(t *testing.T, doc string, fields ...string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, doc)
	}
	out := map[string]any{}
	for _, f := range fields {
		v, ok := m[f]
		if !ok {
			t.Errorf("field %q missing from\n%s", f, doc)
		}
		out[f] = v
	}
	return out
}

func listFixtures(t *testing.T, dirs ...string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		entries, err := os.ReadDir(filepath.Join(driftTestdataDir, d))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			out = append(out, filepath.Join(driftTestdataDir, d, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// driftDivergences are the fixtures where mars exits 1 on purpose and the
// script does not. Each is a plan terraform does not write.
var driftDivergences = map[string]string{
	"empty_plan.json":         "empty show output: the script read it as no drift; mars fails",
	"whitespace_plan.json":    "blank show output: as empty_plan.json",
	"object_iter_plan.json":   "resource_drift is an object: jq iterated its values; mars wants an array",
	"array_before_plan.json":  "change.before is an array: jq listed it by index; mars wants an object",
	"actions_false_plan.json": "actions is false: jq read it as actionable; mars wants an array of strings",
}

var driftReportFields = []string{"drift_detected", "drift_count", "actionable", "apply_skipped", "resources"}

func TestDriftCheckFieldParityWithShellScript(t *testing.T) {
	requireShellTools(t)
	script := filepath.Join(driftTestdataDir, "driftcheck", "drift-check.sh")
	flagSets := [][]string{nil, {"--ignore-drift"}, {"--strict"}, {"--strict", "--ignore-drift"}}
	for _, fixture := range listFixtures(t, "driftcheck/plans") {
		base := filepath.Base(fixture)
		for _, flags := range flagSets {
			t.Run(base+"/"+strings.Join(flags, "_"), func(t *testing.T) {
				root := setupShellEnv(t, fixture)
				args := append([]string{"test.tfplan"}, flags...)
				sh := runShell(t, script, args...)
				driftJSON, readErr := os.ReadFile(filepath.Join(root, "outputs", "drift.json"))
				gor := runMars(t, append([]string{"drift-check"}, args...)...)

				if why, ok := driftDivergences[base]; ok {
					if gor.code != 1 {
						t.Errorf("mars exit %d, want 1 (%s)\nstderr: %s", gor.code, why, gor.stderr)
					}
					return
				}
				if sh.code != 0 && sh.code != 2 {
					if gor.code != 1 {
						t.Errorf("script failed (exit %d), mars exit %d, want 1\nscript stderr: %s\nmars stderr: %s", sh.code, gor.code, sh.stderr, gor.stderr)
					}
					return
				}
				ignore := slicesContains(flags, "--ignore-drift")
				strict := slicesContains(flags, "--strict")
				got := parseFields(t, gor.stdout, append(driftReportFields, "actionable_count")...)
				if readErr != nil {
					// The script found no drift and wrote nothing.
					if got["drift_detected"] != false || got["drift_count"] != float64(0) || gor.code != 0 {
						t.Errorf("script found no drift; mars exit %d, report %v", gor.code, got)
					}
					return
				}
				want := parseFields(t, string(driftJSON), driftReportFields...)
				for _, f := range driftReportFields {
					if !reflect.DeepEqual(want[f], got[f]) {
						t.Errorf("field %s: script %v, mars %v", f, want[f], got[f])
					}
				}
				if (got["actionable_count"].(float64) > 0) != got["actionable"] {
					t.Errorf("actionable_count %v disagrees with actionable %v", got["actionable_count"], got["actionable"])
				}
				wantCode := 0
				if !ignore && (strict || want["actionable"] == true) {
					wantCode = 2
				}
				if gor.code != wantCode {
					t.Errorf("mars exit %d, want %d (script exit %d)\nstderr: %s", gor.code, wantCode, sh.code, gor.stderr)
				}
				if strict && !ignore && sh.code != 2 {
					t.Errorf("script exit %d with --strict and drift, want 2", sh.code)
				}
			})
		}
	}
}

// planSummaryDivergences are the fixtures where mars's result differs from
// the script's on purpose, with mars's exit code; each is a plan terraform
// does not write.
var planSummaryDivergences = map[string]struct {
	code int
	why  string
}{
	"empty_plan.json":             {1, "empty show output: jq printed nothing; mars fails"},
	"whitespace_plan.json":        {1, "blank show output: as empty_plan.json"},
	"object_iter_plan.json":       {1, "resource_changes is an object: jq iterated its values; mars wants an array"},
	"rc_address_number_plan.json": {1, "a numeric address: jq did not read it; mars wants a string"},
	"null_change_plan.json":       {0, "actions null: jq's contains failed; mars counts nothing for it"},
	"action_missing_plan.json":    {0, "no actions: as null_change_plan.json"},
	"no_actions_plan.json":        {0, "no actions: as null_change_plan.json"},
}

var planSummaryFields = []string{"add", "change", "destroy", "import", "has_changes"}

func TestPlanSummaryFieldParityWithShellScript(t *testing.T) {
	requireShellTools(t)
	script := filepath.Join(driftTestdataDir, "plansummary", "counts.sh")
	for _, fixture := range listFixtures(t, "driftcheck/plans", "plansummary/plans") {
		base := filepath.Base(fixture)
		for _, input := range []string{"binary", "json"} {
			t.Run(base+"/"+input, func(t *testing.T) {
				setupShellEnv(t, fixture)
				sh := runShell(t, script, fixture)
				planArg := "test.tfplan"
				if input == "json" {
					planArg = fixture
				}
				gor := runMars(t, "plan-summary", planArg)
				if d, ok := planSummaryDivergences[base]; ok {
					if gor.code != d.code {
						t.Errorf("mars exit %d, want %d (%s)\nstderr: %s", gor.code, d.code, d.why, gor.stderr)
					}
					return
				}
				if sh.code != 0 {
					if gor.code != 1 {
						t.Errorf("script failed (exit %d), mars exit %d, want 1\nscript stderr: %s\nmars stdout: %s", sh.code, gor.code, sh.stderr, gor.stdout)
					}
					return
				}
				if gor.code != 0 {
					t.Fatalf("mars exit %d, script 0\nstderr: %s", gor.code, gor.stderr)
				}
				want := parseFields(t, sh.stdout, planSummaryFields...)
				got := parseFields(t, gor.stdout, append(planSummaryFields, "replace", "addresses")...)
				for _, f := range planSummaryFields {
					if !reflect.DeepEqual(want[f], got[f]) {
						t.Errorf("field %s: script %v, mars %v", f, want[f], got[f])
					}
				}
			})
		}
	}
}

func slicesContains(s []string, x string) bool {
	for _, e := range s {
		if e == x {
			return true
		}
	}
	return false
}
