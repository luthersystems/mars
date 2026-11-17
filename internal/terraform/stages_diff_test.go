package terraform_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The stages differential tests run the template's multi-stage scripts and
// the mars stages commands on the same fake project and compare what they
// write (decoded JSON, field for field) and their exit codes:
//
//   - plan-all.sh (with plan.sh) and mars plan-all;
//   - apply-plan.sh <stage> --plan-file job and mars apply-stage <stage>
//     --plan job.tfplan --outputs --clean-providers;
//   - apply-with-outputs.sh <stage> --check-drift and mars apply-stage
//     <stage> --check-drift --outputs;
//   - drift-refresh.sh <stage> and mars plan-stage <stage> --refresh-only
//     --check-drift --strict --plan-file refresh.tfplan.
//
// testdata/stages/template/tf holds those scripts and utils.sh, copied
// verbatim from sandbox-infrastructure-template commit
// d39c011dca686c2e8af8874c8b88e4ff7d7b54f9; drift-check.sh is
// testdata/driftcheck's copy. testdata/stages/template/shell_utils.sh stands
// in for the template's (no credentials, no git), run-with-creds.sh for its
// mars wrapper, and fake-terraform for terraform, which both sides run.
// Recopy the scripts when the template changes them.

const stagesTestConfig = `version: 1
drift:
  stamp_env:
    template_version: TEMPLATE_VERSION
    presets_version: PRESETS_VERSION
stages:
  - {name: cloud-provision, dir: tf/cloud-provision, apply_if_no_state: true}
  - {name: custom-stack-provision, dir: tf/custom-stack-provision, plan_alias_preferred: true}
  - {name: stage-a, dir: tf/stage-a}
  - {name: stage-b, dir: tf/stage-b}
`

var stageNames = []string{"cloud-provision", "custom-stack-provision", "stage-a", "stage-b"}

// stageFixtures is a fixture directory for fake-terraform.
type stageFixtures string

func (d stageFixtures) set(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(string(d), name), []byte(content), 0o644); err != nil { //nolint:gosec // G703: a fixture name the test chose, in its own temp dir
		t.Fatal(err)
	}
}

func (d stageFixtures) remove(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(string(d), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// stagesTestEnv puts fake-terraform first on PATH and returns the fixture
// directory.
func stagesTestEnv(t *testing.T) stageFixtures {
	t.Helper()
	requireShellTools(t)
	work := t.TempDir()
	bin := filepath.Join(work, "bin")
	fixtures := filepath.Join(work, "fixtures")
	for _, d := range []string{bin, fixtures} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyTestFile(t, filepath.Join(driftTestdataDir, "stages", "fake-terraform"), filepath.Join(bin, "terraform"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_STAGES", fixtures)
	t.Setenv("HOME", work)
	t.Setenv("TEMPLATE_VERSION", "tmpl-1")
	t.Setenv("PRESETS_VERSION", "")
	t.Setenv("TF_LOG", "")
	return stageFixtures(fixtures)
}

func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil { //nolint:gosec // G306: the copied scripts must be executable
		t.Fatal(err)
	}
}

// stagesProject makes a project with the template's scripts, the stage
// directories and mars-stages.yaml.
func stagesProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	tmpl := filepath.Join(driftTestdataDir, "stages", "template")
	for _, f := range []string{"plan-all.sh", "plan.sh", "utils.sh", "apply-plan.sh", "apply-with-outputs.sh", "drift-refresh.sh"} {
		copyTestFile(t, filepath.Join(tmpl, "tf", f), filepath.Join(root, "tf", f))
	}
	copyTestFile(t, filepath.Join(tmpl, "shell_utils.sh"), filepath.Join(root, "shell_utils.sh"))
	copyTestFile(t, filepath.Join(driftTestdataDir, "stages", "run-with-creds.sh"), filepath.Join(root, "tf", "run-with-creds.sh"))
	copyTestFile(t, filepath.Join(driftTestdataDir, "driftcheck", "drift-check.sh"), filepath.Join(root, "tf", "drift-check.sh"))
	dirs := []string{filepath.Join(root, "tf", "auto-vars", "vars", "default"), filepath.Join(root, "outputs")}
	for _, s := range stageNames {
		dirs = append(dirs, filepath.Join(root, "tf", s, "vars", "default"))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "mars-stages.yaml"), []byte(stagesTestConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// runIn runs a command in dir, logging fake-terraform's calls to log.
func runIn(t *testing.T, dir, log string, env []string, argv ...string) cmdResult {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // G204: runs the test's own fixture scripts with fixed args
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "FAKE_TF_LOG="+log, "MARS_PROJECT_ROOT="+filepath.Dir(dir)), env...)
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

// runMarsIn runs the mars CLI in root, logging fake-terraform's calls to log.
func runMarsIn(t *testing.T, root, log string, args ...string) cmdResult {
	t.Helper()
	t.Setenv("FAKE_TF_LOG", log)
	t.Chdir(root)
	return runMars(t, args...)
}

// outputsJSON reads every JSON file in the project's outputs directory.
func outputsJSON(t *testing.T, root string) map[string]any {
	t.Helper()
	out := map[string]any{}
	matches, _ := filepath.Glob(filepath.Join(root, "outputs", "*.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		out[filepath.Base(m)] = v
	}
	return out
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// appliedStages are the stages fake-terraform ran apply in.
func appliedStages(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[1] == "apply" {
			out = append(out, f[0])
		}
	}
	return out
}

func TestPlanAllParityWithShellScript(t *testing.T) {
	type scenario struct {
		name     string
		stages   string
		fixtures map[string]string
		// files are fixture files with no content (fail, has-state, ...).
		files []string
	}
	creates := `{"resource_changes": [
  {"address": "aws_instance.web", "change": {"actions": ["create"]}},
  {"address": "aws_ebs_volume.data", "change": {"actions": ["create"]}},
  {"address": "aws_security_group.web", "change": {"actions": ["update"]}},
  {"address": "aws_vpc.main", "change": {"actions": ["no-op"]}}]}`
	cloud := `{"resource_changes": [
  {"address": "aws_route53_zone.main", "change": {"actions": ["create"]}},
  {"address": "aws_s3_bucket.state", "change": {"actions": ["update"]}},
  {"address": "aws_s3_bucket.old", "change": {"actions": ["delete", "create"]}},
  {"address": "data.x.y", "change": {"actions": ["read"]}}]}`
	imports := `{"resource_changes": [
  {"address": "aws_s3_bucket.a", "change": {"actions": ["import"]}},
  {"address": "aws_s3_bucket.b", "change": {"actions": ["import", "update"]}}]}`
	defaultStages := "cloud-provision custom-stack-provision"
	scenarios := []scenario{
		{name: "no changes", stages: defaultStages, files: []string{"cloud-provision.has-state"}},
		{name: "one stage", stages: defaultStages, fixtures: map[string]string{"custom-stack-provision": creates}, files: []string{"cloud-provision.has-state"}},
		{name: "both stages", stages: defaultStages, fixtures: map[string]string{"cloud-provision": cloud, "custom-stack-provision": creates}, files: []string{"cloud-provision.has-state"}},
		{name: "imports", stages: defaultStages, fixtures: map[string]string{"custom-stack-provision": imports}, files: []string{"cloud-provision.has-state"}},
		{name: "failed stage", stages: "stage-a stage-b", fixtures: map[string]string{"stage-b": creates}, files: []string{"stage-a.fail"}},
		{name: "no alias stage", stages: "stage-a stage-b", fixtures: map[string]string{"stage-a": cloud}},
		{name: "all failed", stages: "stage-a stage-b", files: []string{"stage-a.fail", "stage-b.fail"}},
		{name: "new project", stages: defaultStages, fixtures: map[string]string{"cloud-provision": cloud}},
		{name: "new project apply fails", stages: defaultStages, fixtures: map[string]string{"cloud-provision": cloud}, files: []string{"cloud-provision.apply-fail"}},
		{name: "failed preferred stage", stages: defaultStages, fixtures: map[string]string{"cloud-provision": cloud}, files: []string{"cloud-provision.has-state", "custom-stack-provision.fail"}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			fx := stagesTestEnv(t)
			for stage, plan := range sc.fixtures {
				fx.set(t, stage+".json", plan)
			}
			for _, f := range sc.files {
				fx.set(t, f, "")
			}
			shRoot, marsRoot := stagesProject(t), stagesProject(t)
			shLog, marsLog := filepath.Join(shRoot, "tf.log"), filepath.Join(marsRoot, "tf.log")
			sh := runIn(t, filepath.Join(shRoot, "tf"), shLog, []string{"PLAN_STAGES=" + sc.stages}, "bash", "plan-all.sh")
			gor := runMarsIn(t, marsRoot, marsLog, "plan-all", "--stages", strings.ReplaceAll(sc.stages, " ", ","))
			if sh.code != gor.code {
				t.Errorf("exit: script %d, mars %d\nscript: %s%s\nmars: %s", sh.code, gor.code, sh.stdout, sh.stderr, gor.stderr)
			}
			want, got := outputsJSON(t, shRoot), outputsJSON(t, marsRoot)
			if _, ok := want["plan-summary.json"]; !ok {
				t.Fatalf("the script wrote no summary: %s%s", sh.stdout, sh.stderr)
			}
			if !reflect.DeepEqual(keysOf(want), keysOf(got)) {
				t.Errorf("outputs: script %v, mars %v", keysOf(want), keysOf(got))
			}
			for name, w := range want {
				if !reflect.DeepEqual(w, got[name]) {
					t.Errorf("%s:\nscript %v\nmars   %v", name, w, got[name])
				}
			}
			if a, b := appliedStages(t, shLog), appliedStages(t, marsLog); !reflect.DeepEqual(a, b) {
				t.Errorf("applied: script %v, mars %v", a, b)
			}
		})
	}
}

func TestPlanAllContinueParityWithShellScript(t *testing.T) {
	fx := stagesTestEnv(t)
	fx.set(t, "cloud-provision.json", `{"resource_changes": [{"address": "a.b", "change": {"actions": ["create"]}}]}`)
	fx.set(t, "cloud-provision.has-state", "")
	shRoot, marsRoot := stagesProject(t), stagesProject(t)
	shLog, marsLog := filepath.Join(shRoot, "tf.log"), filepath.Join(marsRoot, "tf.log")
	steps := []struct {
		stage string
		cont  bool
	}{{"cloud-provision", false}, {"custom-stack-provision", true}, {"stage-a", true}, {"custom-stack-provision", false}}
	for _, st := range steps {
		env := []string{"PLAN_STAGES=" + st.stage, "PLAN_ALL_CONTINUE=" + map[bool]string{true: "true", false: "false"}[st.cont]}
		sh := runIn(t, filepath.Join(shRoot, "tf"), shLog, env, "bash", "plan-all.sh")
		args := []string{"plan-all", "--stages", st.stage}
		if st.cont {
			args = append(args, "--continue")
		}
		gor := runMarsIn(t, marsRoot, marsLog, args...)
		if sh.code != 0 || gor.code != 0 {
			t.Fatalf("%v: exit script %d, mars %d\n%s", st, sh.code, gor.code, gor.stderr)
		}
		want, got := outputsJSON(t, shRoot), outputsJSON(t, marsRoot)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("after %v:\nscript %v\nmars   %v", st, want, got)
		}
	}
}

// applyParityFixtures are drift-check plan fixtures the apply comparisons
// run on: no drift, actionable drift, drift with a no-op change.
var applyParityFixtures = []string{"noop_plan.json", "actionable_plan.json", "noop_drift_plan.json", "false_positive_plan.json", "enrich_plan.json"}

// driftParityFields are the template's drift.json fields.
var driftParityFields = []string{"drift_detected", "drift_count", "actionable", "apply_skipped", "template_version", "presets_version", "resources"}

// compareApplyRuns compares two apply runs: exit codes, applied stages,
// outputs, and the drift reports' template fields.
func compareApplyRuns(t *testing.T, stage string, sh, gor cmdResult, shRoot, marsRoot string) {
	t.Helper()
	if sh.code != gor.code {
		t.Errorf("exit: script %d, mars %d\nscript: %s%s\nmars: %s", sh.code, gor.code, sh.stdout, sh.stderr, gor.stderr)
	}
	if a, b := appliedStages(t, filepath.Join(shRoot, "tf.log")), appliedStages(t, filepath.Join(marsRoot, "tf.log")); !reflect.DeepEqual(a, b) {
		t.Errorf("applied: script %v, mars %v", a, b)
	}
	want, got := outputsJSON(t, shRoot), outputsJSON(t, marsRoot)
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("mars did not write %s", name)
			continue
		}
		if strings.HasPrefix(name, "drift") {
			wm, gm := w.(map[string]any), g.(map[string]any)
			for _, f := range driftParityFields {
				if !reflect.DeepEqual(wm[f], gm[f]) {
					t.Errorf("%s field %s: script %v, mars %v", name, f, wm[f], gm[f])
				}
			}
			continue
		}
		if !reflect.DeepEqual(w, g) {
			t.Errorf("%s: script %v, mars %v", name, w, g)
		}
	}
	// mars writes the drift reports even without drift; the script only
	// with drift (and its apply wrappers then a stub). Every other file must
	// match.
	for name, g := range got {
		if _, ok := want[name]; ok {
			continue
		}
		noDrift := false
		if m, ok := g.(map[string]any); ok {
			noDrift = m["drift_detected"] == false
		}
		if name != "drift-"+stage+".json" && (name != "drift.json" || !noDrift) {
			t.Errorf("mars wrote %s, the script did not", name)
		}
	}
}

func TestApplyPlanParityWithShellScript(t *testing.T) {
	const stage = "custom-stack-provision"
	for _, fixture := range applyParityFixtures {
		for _, flags := range [][]string{nil, {"--check-drift"}, {"--check-drift", "--ignore-drift"}} {
			t.Run(fixture+"/"+strings.Join(flags, "_"), func(t *testing.T) {
				fx := stagesTestEnv(t)
				plan, err := os.ReadFile(filepath.Join(driftTestdataDir, "driftcheck", "plans", fixture))
				if err != nil {
					t.Fatal(err)
				}
				fx.set(t, stage+".json", string(plan))
				shRoot, marsRoot := stagesProject(t), stagesProject(t)
				for _, r := range []string{shRoot, marsRoot} {
					if err := os.WriteFile(filepath.Join(r, "tf", stage, "job.tfplan"), []byte("PK"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				sh := runIn(t, filepath.Join(shRoot, "tf"), filepath.Join(shRoot, "tf.log"), nil,
					append([]string{"bash", "apply-plan.sh", stage, "--plan-file", "job"}, flags...)...)
				gor := runMarsIn(t, marsRoot, filepath.Join(marsRoot, "tf.log"),
					append([]string{"apply-stage", stage, "--plan", "job.tfplan", "--outputs", "--clean-providers"}, flags...)...)
				compareApplyRuns(t, stage, sh, gor, shRoot, marsRoot)
			})
		}
	}
	t.Run("missing plan file", func(t *testing.T) {
		stagesTestEnv(t)
		shRoot, marsRoot := stagesProject(t), stagesProject(t)
		sh := runIn(t, filepath.Join(shRoot, "tf"), filepath.Join(shRoot, "tf.log"), nil, "bash", "apply-plan.sh", stage, "--plan-file", "job")
		gor := runMarsIn(t, marsRoot, filepath.Join(marsRoot, "tf.log"), "apply-stage", stage, "--plan", "job.tfplan")
		if sh.code != 1 || gor.code != 1 {
			t.Errorf("exit: script %d, mars %d, want 1", sh.code, gor.code)
		}
	})
	t.Run("apply fails", func(t *testing.T) {
		fx := stagesTestEnv(t)
		fx.set(t, stage+".apply-fail", "")
		shRoot, marsRoot := stagesProject(t), stagesProject(t)
		for _, r := range []string{shRoot, marsRoot} {
			if err := os.WriteFile(filepath.Join(r, "tf", stage, "job.tfplan"), []byte("PK"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sh := runIn(t, filepath.Join(shRoot, "tf"), filepath.Join(shRoot, "tf.log"), nil, "bash", "apply-plan.sh", stage, "--plan-file", "job")
		gor := runMarsIn(t, marsRoot, filepath.Join(marsRoot, "tf.log"), "apply-stage", stage, "--plan", "job.tfplan", "--outputs")
		if sh.code != 1 || gor.code != 1 {
			t.Errorf("exit: script %d, mars %d, want 1", sh.code, gor.code)
		}
		fx.remove(t, stage+".apply-fail")
	})
}

func TestApplyWithOutputsDriftParityWithShellScript(t *testing.T) {
	const stage = "stage-a"
	for _, fixture := range applyParityFixtures {
		for _, flags := range [][]string{{"--check-drift"}, {"--check-drift", "--ignore-drift"}} {
			t.Run(fixture+"/"+strings.Join(flags, "_"), func(t *testing.T) {
				fx := stagesTestEnv(t)
				plan, err := os.ReadFile(filepath.Join(driftTestdataDir, "driftcheck", "plans", fixture))
				if err != nil {
					t.Fatal(err)
				}
				fx.set(t, stage+".json", string(plan))
				shRoot, marsRoot := stagesProject(t), stagesProject(t)
				sh := runIn(t, filepath.Join(shRoot, "tf"), filepath.Join(shRoot, "tf.log"), nil,
					append([]string{"bash", "apply-with-outputs.sh", stage}, flags...)...)
				gor := runMarsIn(t, marsRoot, filepath.Join(marsRoot, "tf.log"),
					append([]string{"apply-stage", stage, "--outputs"}, flags...)...)
				compareApplyRuns(t, stage, sh, gor, shRoot, marsRoot)
			})
		}
	}
}

func TestDriftRefreshParityWithShellScript(t *testing.T) {
	const stage = "stage-b"
	for _, fixture := range applyParityFixtures {
		t.Run(fixture, func(t *testing.T) {
			fx := stagesTestEnv(t)
			plan, err := os.ReadFile(filepath.Join(driftTestdataDir, "driftcheck", "plans", fixture))
			if err != nil {
				t.Fatal(err)
			}
			fx.set(t, stage+".json", string(plan))
			shRoot, marsRoot := stagesProject(t), stagesProject(t)
			sh := runIn(t, filepath.Join(shRoot, "tf"), filepath.Join(shRoot, "tf.log"), nil, "bash", "drift-refresh.sh", stage)
			gor := runMarsIn(t, marsRoot, filepath.Join(marsRoot, "tf.log"),
				"plan-stage", stage, "--refresh-only", "--check-drift", "--strict", "--plan-file", "refresh.tfplan")
			compareApplyRuns(t, stage, sh, gor, shRoot, marsRoot)
			// Both planned refresh-only into refresh.tfplan.
			for _, r := range []string{shRoot, marsRoot} {
				b, _ := os.ReadFile(filepath.Join(r, "tf.log"))
				if !strings.Contains(string(b), "-refresh-only") || !strings.Contains(string(b), "-out=refresh.tfplan") {
					t.Errorf("%s: no refresh-only plan:\n%s", r, b)
				}
			}
		})
	}
}
