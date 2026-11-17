package terraform

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// stageCall is one command a stage command ran, with the stage directory it
// ran in ("" outside one).
type stageCall struct {
	stage string
	argv  []string
	env   map[string]string
	dir   string
}

// stageFake is a runner that answers terraform by stage: the base name of
// the working directory names the stage.
type stageFake struct {
	root  string
	calls []stageCall
	// plans is each stage's terraform show -json output.
	plans map[string]string
	// state is each stage's terraform state list output.
	state map[string]string
	// fail makes "<stage> <verb>" exit 1, for example "a plan".
	fail map[string]bool
	// outputs is each stage's terraform output -json.
	outputs map[string]string
	// workspace is what terraform workspace show prints (default "default").
	workspace string
}

func (f *stageFake) stage() string {
	wd, _ := os.Getwd()
	if filepath.Dir(wd) == filepath.Join(f.root, "stages") {
		return filepath.Base(wd)
	}
	return ""
}

func (f *stageFake) do(cmd runner.Command) ([]byte, error) {
	st := f.stage()
	f.calls = append(f.calls, stageCall{stage: st, argv: cmd.Argv, env: cmd.Env, dir: cmd.Dir})
	verb := ""
	if len(cmd.Argv) > 1 {
		verb = cmd.Argv[1]
	}
	if cmd.Argv[0] != "terraform" {
		if f.fail[cmd.Argv[0]] {
			return nil, &runner.ExitError{Code: 3}
		}
		return nil, nil
	}
	if f.fail[st+" "+verb] {
		return nil, &runner.ExitError{Code: 1}
	}
	switch verb {
	case "workspace":
		if cmd.Argv[2] == "show" {
			if f.workspace != "" {
				return []byte(f.workspace + "\n"), nil
			}
			return []byte("default\n"), nil
		}
	case "show":
		p, ok := f.plans[st]
		if !ok {
			p = `{"resource_changes": []}`
		}
		return []byte(p), nil
	case "state":
		return []byte(f.state[st]), nil
	case "output":
		return []byte(f.outputs[st]), nil
	}
	return nil, nil
}

func (f *stageFake) Run(_ context.Context, cmd runner.Command) error {
	_, err := f.do(cmd)
	return err
}

func (f *stageFake) Capture(_ context.Context, cmd runner.Command) ([]byte, error) {
	return f.do(cmd)
}

func (f *stageFake) Pipe(context.Context, runner.Command, runner.Command) error { return nil }

// terraformCalls are the stage's terraform commands, without "terraform".
func (f *stageFake) terraformCalls(stage string) []string {
	var out []string
	for _, c := range f.calls {
		if c.stage == stage && c.argv[0] == "terraform" {
			out = append(out, strings.Join(c.argv[1:], " "))
		}
	}
	return out
}

func (f *stageFake) ran(stage, prefix string) bool {
	for _, c := range f.terraformCalls(stage) {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

const twoStageConfig = `version: 1
stages:
  - name: a
    dir: stages/a
    apply_if_no_state: true
  - name: b
    dir: stages/b
    plan_alias_preferred: true
`

// stageProject makes a project with the config and stage directories a and
// b (each with vars/default), and changes into it.
func stageProject(t *testing.T, config string, stages ...string) (string, *stageFake) {
	t.Helper()
	root := t.TempDir()
	if len(stages) == 0 {
		stages = []string{"a", "b"}
	}
	for _, s := range stages {
		if err := os.MkdirAll(filepath.Join(root, "stages", s, "vars", "default"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "stages", stages[0], "vars", "default", "x.tfvars"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DefaultStagesConfig), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root, &stageFake{
		root:    root,
		plans:   map[string]string{},
		state:   map[string]string{"a": "aws_s3_bucket.state\n"},
		fail:    map[string]bool{},
		outputs: map[string]string{},
	}
}

func stageRuntime(f *stageFake) (*app.Runtime, *strings.Builder, *strings.Builder) {
	out, errb := &strings.Builder{}, &strings.Builder{}
	return &app.Runtime{Target: "default", Runner: f, Stdin: strings.NewReader(""), Stdout: out, Stderr: errb, SkipPrompt: true}, out, errb
}

func readJSONFile(t *testing.T, path string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return v
}

func mustDecode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	return v
}

const (
	planCreate2Update1 = `{"resource_changes": [
  {"address": "x.one", "change": {"actions": ["create"]}},
  {"address": "x.two", "change": {"actions": ["create"]}},
  {"address": "x.three", "change": {"actions": ["update"]}},
  {"address": "x.four", "change": {"actions": ["no-op"]}}]}`
	planDelete1 = `{"resource_changes": [{"address": "y.one", "change": {"actions": ["delete"]}}]}`
	planDrift   = `{"resource_drift": [{"address": "z.one", "change": {"actions": ["update"], "before": {"v": 1}, "after": {"v": 2}}}],
  "resource_changes": [{"address": "z.one", "change": {"actions": ["update"]}}]}`
)

func TestLoadStagesConfigDefaults(t *testing.T) {
	root, _ := stageProject(t, twoStageConfig)
	cfg, err := loadStagesConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.planFile() != "mars.tfplan" || !reflect.DeepEqual(cfg.initArgs(), []string{"-input=false", "-reconfigure"}) || !cfg.continueOnError() {
		t.Errorf("defaults: plan_file %q init_args %v continue %v", cfg.planFile(), cfg.initArgs(), cfg.continueOnError())
	}
	want := map[string]string{
		cfg.outPath(cfg.planJSONName(), "a"):     filepath.Join(root, "outputs", "tfplan-a.json"),
		cfg.outPath(cfg.planAliasName(), ""):     filepath.Join(root, "outputs", "tfplan.json"),
		cfg.outPath(cfg.summaryName(), ""):       filepath.Join(root, "outputs", "plan-summary.json"),
		cfg.outPath(cfg.driftName(), "a"):        filepath.Join(root, "outputs", "drift-a.json"),
		cfg.outPath(cfg.driftAliasName(), ""):    filepath.Join(root, "outputs", "drift.json"),
		cfg.outPath(cfg.outputsName(), "a"):      filepath.Join(root, "outputs", "a.json"),
		cfg.outPath(cfg.outputsAliasName(), ""):  filepath.Join(root, "outputs", "outputs.json"),
		cfg.stageDir(cfg.stage("b")):             filepath.Join(root, "stages", "b"),
		cfg.outPath(cfg.stageSummaryName(), "a"): "",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("path %q, want %q", got, w)
		}
	}
}

func TestLoadStagesConfigRejects(t *testing.T) {
	cases := map[string]string{
		"version":         "version: 2\nstages: [{name: a}]\n",
		"no stages":       "version: 1\n",
		"unknown key":     "version: 1\nstagez: []\nstages: [{name: a}]\n",
		"bad name":        "version: 1\nstages: [{name: 'a/b'}]\n",
		"duplicate":       "version: 1\nstages: [{name: a}, {name: a}]\n",
		"dir escapes":     "version: 1\nstages: [{name: a, dir: ../x}]\n",
		"no {stage}":      "version: 1\nfiles: {plan_json: plan.json}\nstages: [{name: a}]\n",
		"alias {stage}":   "version: 1\nfiles: {summary: 's-{stage}.json'}\nstages: [{name: a}]\n",
		"glob":            "version: 1\nfiles: {drift: 'd*-{stage}.json'}\nstages: [{name: a}]\n",
		"two preferred":   "version: 1\nstages: [{name: a, plan_alias_preferred: true}, {name: b, plan_alias_preferred: true}]\n",
		"stamp reserved":  "version: 1\ndrift: {stamp_env: {resources: X}}\nstages: [{name: a}]\n",
		"stamp count":     "version: 1\ndrift: {stamp_env: {actionable_count: X}}\nstages: [{name: a}]\n",
		"kinds collide":   "version: 1\nfiles: {plan_json: 'x-{stage}.json', drift: 'x-{stage}.json'}\nstages: [{name: a}]\n",
		"stage = global":  "version: 1\nstages: [{name: a}, {name: outputs}]\n",
		"globals collide": "version: 1\nfiles: {summary: tfplan.json}\nstages: [{name: a}]\n",
		"stage = alias":   "version: 1\nfiles: {plan_json: '{stage}.plan.json', plan_alias: tfplan.plan.json}\nstages: [{name: tfplan}]\n",
		"stamp env name":  "version: 1\ndrift: {stamp_env: {v: 'not a name'}}\nstages: [{name: a}]\n",
		"plan_file path":  "version: 1\nplan_file: ../p.tfplan\nstages: [{name: a}]\n",
		"outputs escape":  "version: 1\noutputs_dir: ../out\nstages: [{name: a}]\n",
		"empty":           "",
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "s.yaml")
			if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadStagesConfig(p); err == nil {
				t.Errorf("config accepted:\n%s", cfg)
			}
		})
	}
}

func TestPlanAllPlansEachStageAndWritesSummary(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planDelete1
	f.plans["b"] = planCreate2Update1
	rt, stdout, stderr := stageRuntime(f)

	if err := (&PlanAllCmd{}).Run(context.Background(), rt); err != nil {
		t.Fatalf("plan-all: %v\n%s", err, stderr)
	}
	wantA := []string{
		"workspace show",
		"init -input=false -reconfigure",
		"workspace select default",
		"plan -var-file=vars/default/x.tfvars -input=false -out=mars.tfplan",
		"show -json mars.tfplan",
		"state list",
	}
	if got := f.terraformCalls("a"); !reflect.DeepEqual(got, wantA) {
		t.Errorf("stage a ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantA, "\n"))
	}
	if f.ran("b", "state list") {
		t.Error("stage b has no apply_if_no_state but its state was listed")
	}
	want := mustDecode(t, `{
  "stages": {
    "a": {"add": 0, "change": 0, "destroy": 1, "import": 0, "has_changes": true},
    "b": {"add": 2, "change": 1, "destroy": 0, "import": 0, "has_changes": true}
  },
  "total": {"add": 2, "change": 1, "destroy": 1, "import": 0},
  "has_changes": true
}`)
	if got := readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")); !reflect.DeepEqual(got, want) {
		t.Errorf("summary %v, want %v", got, want)
	}
	if got := mustDecode(t, stdout.String()); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout %v, want the summary", got)
	}
	b, _ := os.ReadFile(filepath.Join(root, "outputs", "plan-summary.json"))
	if !strings.Contains(string(b), "\"a\": {\n      \"add\": 0,\n      \"change\": 0,\n      \"destroy\": 1,\n      \"import\": 0,\n      \"has_changes\": true\n    },\n    \"b\"") {
		t.Errorf("summary is not in the template's field order:\n%s", b)
	}
	if got := readJSONFile(t, filepath.Join(root, "outputs", "tfplan.json")); !reflect.DeepEqual(got, mustDecode(t, planCreate2Update1)) {
		t.Errorf("tfplan.json is not stage b's (preferred) plan: %v", got)
	}
	if got := readJSONFile(t, filepath.Join(root, "outputs", "tfplan-a.json")); !reflect.DeepEqual(got, mustDecode(t, planDelete1)) {
		t.Errorf("tfplan-a.json %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "outputs", "drift-a.json")); err == nil {
		t.Error("drift report written without --check-drift")
	}
}

func TestPlanAllAppliesNewProjectStage(t *testing.T) {
	_, f := stageProject(t, twoStageConfig)
	f.state["a"] = "\n"
	rt, _, stderr := stageRuntime(f)
	if err := (&PlanAllCmd{}).Run(context.Background(), rt); err != nil {
		t.Fatalf("plan-all: %v\n%s", err, stderr)
	}
	var apply *stageCall
	for i, c := range f.calls {
		if c.stage == "a" && c.argv[0] == "terraform" && c.argv[1] == "apply" {
			apply = &f.calls[i]
		}
	}
	if apply == nil {
		t.Fatalf("stage a not applied:\n%v", f.terraformCalls("a"))
	}
	if got := strings.Join(apply.argv, " "); got != "terraform apply -input=false mars.tfplan" {
		t.Errorf("apply = %s", got)
	}
	if !reflect.DeepEqual(apply.env, map[string]string{"TF_CLI_ARGS_plan": "", "TF_CLI_ARGS_apply": ""}) {
		t.Errorf("apply env = %v", apply.env)
	}
	if !strings.Contains(stderr.String(), "a applied successfully") {
		t.Errorf("stderr:\n%s", stderr)
	}
	// b plans after a's apply.
	idx := func(stage, prefix string) int {
		for i, c := range f.calls {
			if c.stage == stage && c.argv[0] == "terraform" && strings.HasPrefix(strings.Join(c.argv[1:], " "), prefix) {
				return i
			}
		}
		return -1
	}
	if idx("b", "plan") < idx("a", "apply") {
		t.Error("stage b planned before stage a applied")
	}
}

func TestPlanAllNewProjectApplyFailure(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.state["a"] = ""
	f.fail["a apply"] = true
	f.plans["a"] = planDelete1
	rt, _, stderr := stageRuntime(f)
	err := (&PlanAllCmd{}).Run(context.Background(), rt)
	if runner.ExitCode(err) != 1 {
		t.Fatalf("exit %d, want 1", runner.ExitCode(err))
	}
	if !f.ran("b", "plan") {
		t.Error("stage b not planned after a's failed apply")
	}
	sum := readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")).(map[string]any)
	if a := sum["stages"].(map[string]any)["a"].(map[string]any); a["destroy"] != float64(1) {
		t.Errorf("a's row lost its counts: %v", a)
	}
	if !strings.Contains(stderr.String(), "terraform apply failed") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestPlanAllStateListFailureIsAnError(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.fail["a state"] = true
	rt, _, _ := stageRuntime(f)
	if code := runner.ExitCode((&PlanAllCmd{}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if f.ran("a", "apply") {
		t.Error("applied although state list failed")
	}
	sum := readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")).(map[string]any)
	if a := sum["stages"].(map[string]any)["a"]; !reflect.DeepEqual(a, map[string]any{"error": true}) {
		t.Errorf("row a = %v", a)
	}
}

func TestPlanAllContinuesAfterAFailedStage(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.fail["a plan"] = true
	f.plans["b"] = planCreate2Update1
	rt, _, _ := stageRuntime(f)
	if code := runner.ExitCode((&PlanAllCmd{}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	want := mustDecode(t, `{"stages": {"a": {"error": true}, "b": {"add": 2, "change": 1, "destroy": 0, "import": 0, "has_changes": true}},
  "total": {"add": 2, "change": 1, "destroy": 0, "import": 0}, "has_changes": true}`)
	if got := readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")); !reflect.DeepEqual(got, want) {
		t.Errorf("summary %v", got)
	}
	if fileExists(filepath.Join(root, "outputs", "tfplan-a.json")) {
		t.Error("a failed stage has plan JSON")
	}
}

func TestPlanAllStopsWhenContinueOnErrorIsOff(t *testing.T) {
	root, f := stageProject(t, "continue_on_error: false\n"+twoStageConfig)
	f.fail["a init"] = true
	rt, _, _ := stageRuntime(f)
	if code := runner.ExitCode((&PlanAllCmd{}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if len(f.terraformCalls("b")) != 0 {
		t.Errorf("stage b ran: %v", f.terraformCalls("b"))
	}
	sum := readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")).(map[string]any)
	if _, ok := sum["stages"].(map[string]any)["b"]; ok {
		t.Error("summary has a row for the stage that did not run")
	}
}

func TestPlanAllStagesFlag(t *testing.T) {
	_, f := stageProject(t, twoStageConfig)
	rt, _, _ := stageRuntime(f)
	if err := (&PlanAllCmd{Stages: []string{"b"}}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if len(f.terraformCalls("a")) != 0 || !f.ran("b", "plan") {
		t.Errorf("--stages b ran a %v, b %v", f.terraformCalls("a"), f.terraformCalls("b"))
	}
	if code := runner.ExitCode((&PlanAllCmd{Stages: []string{"c"}}).Run(context.Background(), rt)); code != 1 {
		t.Errorf("unknown stage: exit %d, want 1", code)
	}
}

func TestPlanAllContinueKeepsEarlierStages(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planDelete1
	rt, _, _ := stageRuntime(f)
	if err := (&PlanAllCmd{Stages: []string{"a"}}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if err := (&PlanAllCmd{Stages: []string{"b"}, Continue: true}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	sum := readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")).(map[string]any)
	if got := len(sum["stages"].(map[string]any)); got != 2 || sum["has_changes"] != true {
		t.Errorf("--continue summary %v", sum)
	}
	if !fileExists(filepath.Join(root, "outputs", "tfplan-a.json")) {
		t.Error("--continue removed the earlier stage's plan JSON")
	}
	// Stage b (preferred) has a plan, so the alias is b's.
	if got := readJSONFile(t, filepath.Join(root, "outputs", "tfplan.json")); !reflect.DeepEqual(got, mustDecode(t, `{"resource_changes": []}`)) {
		t.Errorf("alias %v", got)
	}
	// Without --continue a run starts clean.
	if err := (&PlanAllCmd{Stages: []string{"b"}}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	sum = readJSONFile(t, filepath.Join(root, "outputs", "plan-summary.json")).(map[string]any)
	if got := len(sum["stages"].(map[string]any)); got != 1 {
		t.Errorf("summary kept earlier stages without --continue: %v", sum)
	}
	if fileExists(filepath.Join(root, "outputs", "tfplan-a.json")) {
		t.Error("stale tfplan-a.json kept without --continue")
	}
}

func TestPlanAllAliasFallsBackToLastStageWithPlan(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planDelete1
	f.fail["b plan"] = true
	rt, _, _ := stageRuntime(f)
	_ = (&PlanAllCmd{}).Run(context.Background(), rt)
	if got := readJSONFile(t, filepath.Join(root, "outputs", "tfplan.json")); !reflect.DeepEqual(got, mustDecode(t, planDelete1)) {
		t.Errorf("alias %v, want stage a's plan", got)
	}
}

func TestPlanAllSetupHookAndStripProviders(t *testing.T) {
	cfg := `version: 1
strip_providers: true
setup: [hook, all]
stages:
  - {name: a, dir: stages/a}
  - {name: b, dir: stages/b, setup: [hook, own]}
`
	root, f := stageProject(t, cfg)
	prov := filepath.Join(root, "stages", "a", ".terraform", "providers", "registry")
	mods := filepath.Join(root, "stages", "a", ".terraform", "modules")
	deep := filepath.Join(root, "outputs", "x", ".terraform", "providers")
	for _, d := range []string{prov, mods, deep} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rt, _, stderr := stageRuntime(f)
	if err := (&PlanAllCmd{}).Run(context.Background(), rt); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var hooks []stageCall
	for _, c := range f.calls {
		if c.argv[0] == "hook" {
			hooks = append(hooks, c)
		}
	}
	if len(hooks) != 2 || hooks[0].argv[1] != "all" || hooks[1].argv[1] != "own" {
		t.Fatalf("hooks %v", hooks)
	}
	wantEnv := map[string]string{"MARS_ENV": "default", "MARS_STAGE": "a", "MARS_STAGE_DIR": filepath.Join(root, "stages", "a")}
	if !reflect.DeepEqual(hooks[0].env, wantEnv) || hooks[0].dir != root {
		t.Errorf("hook env %v dir %s", hooks[0].env, hooks[0].dir)
	}
	for _, d := range []string{filepath.Dir(prov), deep} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s not stripped", d)
		}
	}
	if _, err := os.Stat(mods); err != nil {
		t.Errorf("modules removed: %v", err)
	}

	f.fail["hook"] = true
	if code := runner.ExitCode((&PlanAllCmd{}).Run(context.Background(), rt)); code != 1 {
		t.Errorf("failed hook: exit %d, want 1", code)
	}
}

func TestPlanAllCheckDriftWritesStageReports(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planDrift
	rt, _, _ := stageRuntime(f)
	if err := (&PlanAllCmd{CheckDrift: true}).Run(context.Background(), rt); err != nil {
		t.Fatalf("drift must not fail plan-all: %v", err)
	}
	rep := readJSONFile(t, filepath.Join(root, "outputs", "drift-a.json")).(map[string]any)
	if rep["actionable"] != true || rep["apply_skipped"] != true {
		t.Errorf("drift-a.json %v", rep)
	}
	// The alias combines this run's stages: a's drift, b's none.
	alias := readJSONFile(t, filepath.Join(root, "outputs", "drift.json")).(map[string]any)
	if alias["drift_count"] != float64(1) || alias["apply_skipped"] != true || len(alias["resources"].([]any)) != 1 {
		t.Errorf("drift.json %v", alias)
	}
	if rep := readJSONFile(t, filepath.Join(root, "outputs", "drift-b.json")).(map[string]any); rep["drift_detected"] != false {
		t.Errorf("drift-b.json %v", rep)
	}
}

func TestPlanStageDriftRefresh(t *testing.T) {
	root, f := stageProject(t, "drift:\n  stamp_env: {template_version: TV, presets_version: PV}\n"+twoStageConfig)
	t.Setenv("TV", "abc")
	t.Setenv("PV", "")
	f.plans["a"] = planDrift
	rt, _, _ := stageRuntime(f)
	cmd := &PlanStageCmd{Stage: "a", RefreshOnly: true, CheckDrift: true, Strict: true, PlanFile: "refresh.tfplan"}
	if code := runner.ExitCode(cmd.Run(context.Background(), rt)); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !f.ran("a", "plan -var-file=vars/default/x.tfvars -input=false -refresh-only -out=refresh.tfplan") {
		t.Errorf("no refresh-only plan: %v", f.terraformCalls("a"))
	}
	if f.ran("a", "state list") || f.ran("a", "apply") {
		t.Error("plan-stage listed state or applied")
	}
	for _, name := range []string{"drift-a.json", "drift.json"} {
		b, err := os.ReadFile(filepath.Join(root, "outputs", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "\"apply_skipped\": true,\n  \"template_version\": \"abc\",\n  \"presets_version\": null,\n  \"resources\"") {
			t.Errorf("%s does not have the stamps before resources:\n%s", name, b)
		}
	}
	for _, name := range []string{"tfplan-a.json", "tfplan.json"} {
		if !fileExists(filepath.Join(root, "outputs", name)) {
			t.Errorf("%s not written", name)
		}
	}
	if code := runner.ExitCode((&PlanStageCmd{Stage: "a", CheckDrift: true, Strict: true, IgnoreDrift: true}).Run(context.Background(), rt)); code != 0 {
		t.Errorf("--ignore-drift: exit %d, want 0", code)
	}
	f.plans["a"] = planDelete1
	if code := runner.ExitCode((&PlanStageCmd{Stage: "a", CheckDrift: true, Strict: true}).Run(context.Background(), rt)); code != 0 {
		t.Errorf("no drift: exit %d, want 0", code)
	}
	if code := runner.ExitCode((&PlanStageCmd{Stage: "nope"}).Run(context.Background(), rt)); code != 1 {
		t.Errorf("unknown stage: exit %d", code)
	}
}

func TestApplyStageSavedPlan(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.outputs["a"] = `{"bucket": {"value": "b"}}`
	rt, _, stderr := stageRuntime(f)
	planPath := filepath.Join(root, "stages", "a", "job.tfplan")
	prov := filepath.Join(root, "stages", "a", ".terraform", "providers")
	if err := os.MkdirAll(prov, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := &ApplyStageCmd{Stage: "a", Plan: "job.tfplan", Outputs: true, CleanProviders: true}
	if code := runner.ExitCode(cmd.Run(context.Background(), rt)); code != 1 {
		t.Fatalf("missing plan file: exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "plan file not found") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if err := os.WriteFile(planPath, []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(context.Background(), rt); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	want := []string{"workspace show", "init -input=false -reconfigure", "workspace select default", "apply -input=false job.tfplan", "output -json"}
	if got := f.terraformCalls("a"); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	if _, err := os.Stat(prov); err == nil {
		t.Error("--clean-providers left .terraform/providers")
	}
	for _, name := range []string{"a.json", "outputs.json"} {
		if got := readJSONFile(t, filepath.Join(root, "outputs", name)); !reflect.DeepEqual(got, mustDecode(t, f.outputs["a"])) {
			t.Errorf("%s = %v", name, got)
		}
	}
	stub := readJSONFile(t, filepath.Join(root, "outputs", "drift.json"))
	wantStub := mustDecode(t, `{"drift_detected": false, "drift_count": 0, "actionable": false, "actionable_count": 0, "apply_skipped": false, "resources": []}`)
	if !reflect.DeepEqual(stub, wantStub) {
		t.Errorf("drift stub %v", stub)
	}
}

func TestApplyStageDriftGate(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planDrift
	rt, _, stderr := stageRuntime(f)

	// Without --plan it plans first; actionable drift skips the apply.
	cmd := &ApplyStageCmd{Stage: "a", CheckDrift: true, Outputs: true}
	if err := cmd.Run(context.Background(), rt); err != nil {
		t.Fatalf("skipped apply must exit 0: %v\n%s", err, stderr)
	}
	want := []string{"workspace show", "init -input=false -reconfigure", "workspace select default",
		"plan -var-file=vars/default/x.tfvars -input=false -out=mars.tfplan", "show -json mars.tfplan"}
	if got := f.terraformCalls("a"); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	rep := readJSONFile(t, filepath.Join(root, "outputs", "drift.json")).(map[string]any)
	if rep["apply_skipped"] != true {
		t.Errorf("drift.json %v", rep)
	}
	if fileExists(filepath.Join(root, "outputs", "outputs.json")) {
		t.Error("outputs captured although the apply was skipped")
	}

	f.calls = nil
	cmd.IgnoreDrift = true
	if err := cmd.Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if !f.ran("a", "apply -input=false mars.tfplan") || !f.ran("a", "output -json") {
		t.Errorf("--ignore-drift did not apply: %v", f.terraformCalls("a"))
	}
	if rep := readJSONFile(t, filepath.Join(root, "outputs", "drift-a.json")).(map[string]any); rep["apply_skipped"] != false || rep["drift_detected"] != true {
		t.Errorf("drift-a.json %v", rep)
	}

	f.fail["a apply"] = true
	if code := runner.ExitCode(cmd.Run(context.Background(), rt)); code != 1 {
		t.Errorf("failed apply: exit %d, want 1", code)
	}
}

func TestPlanAllContinueKeepsAnEarlierErrorRow(t *testing.T) {
	_, f := stageProject(t, twoStageConfig)
	f.fail["a plan"] = true
	rt, _, _ := stageRuntime(f)
	if code := runner.ExitCode((&PlanAllCmd{Stages: []string{"a"}}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("first step: exit %d, want 1", code)
	}
	if code := runner.ExitCode((&PlanAllCmd{Stages: []string{"b"}, Continue: true}).Run(context.Background(), rt)); code != 1 {
		t.Errorf("--continue after a failed stage: exit %d, want 1 (plan-all.sh's had_error)", code)
	}
}

func TestPlanAllRemovesStaleDriftReports(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	out := filepath.Join(root, "outputs")
	stale := func() {
		t.Helper()
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"drift-a.json", "drift-b.json", "drift.json"} {
			if err := os.WriteFile(filepath.Join(out, n), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	rt, _, _ := stageRuntime(f)

	// Without --continue: every configured stage's, as for plan JSON.
	stale()
	if err := (&PlanAllCmd{Stages: []string{"a"}}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(out, "drift-a.json")) || fileExists(filepath.Join(out, "drift-b.json")) {
		t.Error("stale stage drift report kept without --continue")
	}
	if !fileExists(filepath.Join(out, "drift.json")) {
		t.Error("removed the drift alias without --check-drift")
	}

	// With --continue: only this run's stages'.
	stale()
	if err := (&PlanAllCmd{Stages: []string{"a"}, Continue: true}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(out, "drift-a.json")) || !fileExists(filepath.Join(out, "drift-b.json")) {
		t.Error("--continue: want drift-a.json removed and drift-b.json kept")
	}

	// With --check-drift the alias is this run's.
	stale()
	if err := (&PlanAllCmd{Stages: []string{"a"}, CheckDrift: true}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if rep := readJSONFile(t, filepath.Join(out, "drift.json")).(map[string]any); rep["drift_detected"] != false {
		t.Errorf("drift.json %v, want this run's report", rep)
	}
}

func TestPlanAllFailedStageHasNoPlanJSON(t *testing.T) {
	cases := map[string]func(f *stageFake) *PlanAllCmd{
		"state list": func(f *stageFake) *PlanAllCmd {
			f.fail["a state"] = true
			// The hook writes a's drift report before state list fails.
			return &PlanAllCmd{Stages: []string{"a"}, CheckDrift: true}
		},
		"drift report": func(f *stageFake) *PlanAllCmd {
			f.plans["a"] = `{"resource_drift": {"x": 1}, "resource_changes": []}`
			return &PlanAllCmd{Stages: []string{"a"}, CheckDrift: true}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root, f := stageProject(t, "files: {stage_summary: 'sum-{stage}.json'}\n"+twoStageConfig)
			cmd := setup(f)
			rt, _, _ := stageRuntime(f)
			if code := runner.ExitCode(cmd.Run(context.Background(), rt)); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			for _, n := range []string{"tfplan-a.json", "sum-a.json", "drift-a.json", "tfplan.json"} {
				if fileExists(filepath.Join(root, "outputs", n)) {
					t.Errorf("%s written for a failed stage", n)
				}
			}
			// A failed stage leaves no clean-looking drift alias.
			if fileExists(filepath.Join(root, "outputs", "drift.json")) {
				t.Error("drift.json written by a run with a failed stage")
			}
		})
	}
}

func TestPlanAllPromptsReadPipedAnswersAcrossStages(t *testing.T) {
	_, f := stageProject(t, twoStageConfig)
	f.workspace = "other"
	rt, _, stderr := stageRuntime(f)
	rt.SkipPrompt = false
	rt.Stdin = strings.NewReader("y\ny\n")
	if err := (&PlanAllCmd{}).Run(context.Background(), rt); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !f.ran("a", "plan") || !f.ran("b", "plan") {
		t.Errorf("a %v, b %v", f.terraformCalls("a"), f.terraformCalls("b"))
	}
	if got := strings.Count(stderr.String(), "switch to default?"); got != 2 {
		t.Errorf("prompted %d times, want 2", got)
	}
}

func TestApplyStageCleanProvidersStaysInTheProject(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	outside := t.TempDir()
	kept := filepath.Join(outside, "providers", "registry")
	if err := os.MkdirAll(kept, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "stages", "a", ".terraform")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stages", "a", "job.tfplan"), []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, _, _ := stageRuntime(f)
	cmd := &ApplyStageCmd{Stage: "a", Plan: "job.tfplan", CleanProviders: true}
	if code := runner.ExitCode(cmd.Run(context.Background(), rt)); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("deleted outside the project: %v", err)
	}
	if f.ran("a", "apply") {
		t.Error("applied after the refused clean")
	}
}

func TestStageSetupEmptyListRunsNoHook(t *testing.T) {
	cfg := `version: 1
setup: [hook, all]
stages:
  - {name: a, dir: stages/a, setup: []}
  - {name: b, dir: stages/b}
`
	_, f := stageProject(t, cfg)
	rt, _, _ := stageRuntime(f)
	if err := (&PlanAllCmd{}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, c := range f.calls {
		if c.argv[0] == "hook" {
			stages = append(stages, c.env["MARS_STAGE"])
		}
	}
	if !reflect.DeepEqual(stages, []string{"b"}) {
		t.Errorf("hooks ran for %v, want [b]", stages)
	}
}

func TestPlanAllCleansOnlyConfiguredFiles(t *testing.T) {
	root, f := stageProject(t, "files: {plan_json: 'p-{stage}.json'}\n"+twoStageConfig)
	out := filepath.Join(root, "outputs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"p-zzz.json", "other.json", "p-a.json"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.fail["a plan"] = true
	rt, _, _ := stageRuntime(f)
	_ = (&PlanAllCmd{}).Run(context.Background(), rt)
	if fileExists(filepath.Join(out, "p-a.json")) {
		t.Error("stale p-a.json kept")
	}
	for _, n := range []string{"p-zzz.json", "other.json"} {
		if !fileExists(filepath.Join(out, n)) {
			t.Errorf("removed %s, which no configured stage names", n)
		}
	}
}

func TestDriftDocHasEveryDriftReportField(t *testing.T) {
	_, _ = stageProject(t, "drift: {stamp_env: {stamp: S}}\n"+twoStageConfig)
	cfg, err := loadStagesConfig("")
	if err != nil {
		t.Fatal(err)
	}
	r := &stageRun{cfg: cfg}
	doc, err := r.driftDoc(&DriftReport{})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range doc {
		keys = append(keys, f.Key)
	}
	var want []string
	rt := reflect.TypeFor[DriftReport]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "resources" {
			want = append(want, "stamp")
		}
		want = append(want, name)
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("drift doc fields %v, want %v", keys, want)
	}
	b, _ := marshalNoEscape(doc)
	if !strings.HasSuffix(string(b), `"stamp":null,"resources":[]}`) {
		t.Errorf("doc %s", b)
	}
}

func TestPlanStageFailureLeavesNoReportOrAlias(t *testing.T) {
	root, f := stageProject(t, "files: {stage_summary: 'sum-{stage}.json'}\n"+twoStageConfig)
	out := filepath.Join(root, "outputs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"tfplan.json", "drift.json", "drift-a.json", "tfplan-a.json"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The plan and plan alias are written; the drift report then fails.
	f.plans["a"] = `{"resource_drift": {"x": 1}, "resource_changes": []}`
	rt, _, _ := stageRuntime(f)
	if code := runner.ExitCode((&PlanStageCmd{Stage: "a", CheckDrift: true}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, n := range []string{"tfplan-a.json", "sum-a.json", "drift-a.json", "tfplan.json", "drift.json"} {
		if fileExists(filepath.Join(out, n)) {
			t.Errorf("%s left after the failed plan-stage", n)
		}
	}

	// A failed plan keeps the aliases it never reached.
	for _, n := range []string{"tfplan.json", "drift.json"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.fail["a plan"] = true
	if code := runner.ExitCode((&PlanStageCmd{Stage: "a", CheckDrift: true}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !fileExists(filepath.Join(out, "tfplan.json")) || !fileExists(filepath.Join(out, "drift.json")) {
		t.Error("removed an alias this run did not write")
	}
}

// plan-all --check-drift writes no drift alias, and removes an old one, when
// a stage failed or was never planned (continue_on_error: false stops).
func TestPlanAllCheckDriftNoAliasAfterFailure(t *testing.T) {
	for name, cfg := range map[string]string{
		"failed stage":     twoStageConfig,
		"unplanned stages": "continue_on_error: false\n" + twoStageConfig,
	} {
		t.Run(name, func(t *testing.T) {
			root, f := stageProject(t, cfg)
			out := filepath.Join(root, "outputs")
			if err := os.MkdirAll(out, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "drift.json"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			f.fail["a plan"] = true
			rt, _, _ := stageRuntime(f)
			if code := runner.ExitCode((&PlanAllCmd{CheckDrift: true}).Run(context.Background(), rt)); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			if fileExists(filepath.Join(out, "drift.json")) {
				t.Error("drift.json left after a failed stage")
			}
		})
	}
}

// With --continue --check-drift the alias combines the drift reports of
// every configured stage on disk, so an earlier step's drift stays in it.
func TestPlanAllContinueDriftAliasKeepsEarlierSteps(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planDrift
	rt, _, _ := stageRuntime(f)
	if err := (&PlanAllCmd{Stages: []string{"a"}, CheckDrift: true}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if err := (&PlanAllCmd{Stages: []string{"b"}, Continue: true, CheckDrift: true}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	alias := readJSONFile(t, filepath.Join(root, "outputs", "drift.json")).(map[string]any)
	if alias["drift_count"] != float64(1) || alias["apply_skipped"] != true || len(alias["resources"].([]any)) != 1 {
		t.Errorf("drift.json %v, want stage a's drift", alias)
	}
}

// plan-stage: when show fails after the plan, the aliases are removed.
func TestPlanStageShowFailureRemovesAliases(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	out := filepath.Join(root, "outputs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"tfplan.json", "drift.json"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.fail["a show"] = true
	rt, _, _ := stageRuntime(f)
	if code := runner.ExitCode((&PlanStageCmd{Stage: "a", CheckDrift: true}).Run(context.Background(), rt)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, n := range []string{"tfplan.json", "drift.json"} {
		if fileExists(filepath.Join(out, n)) {
			t.Errorf("%s left after show failed", n)
		}
	}
}

// Without --check-drift, plan-stage neither writes nor removes the stage's
// drift report, on success or failure.
func TestPlanStageKeepsDriftReportWithoutCheckDrift(t *testing.T) {
	for _, failShow := range []bool{false, true} {
		root, f := stageProject(t, twoStageConfig)
		out := filepath.Join(root, "outputs")
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "drift-a.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.fail["a show"] = failShow
		rt, _, _ := stageRuntime(f)
		_ = (&PlanStageCmd{Stage: "a"}).Run(context.Background(), rt)
		if !fileExists(filepath.Join(out, "drift-a.json")) {
			t.Errorf("show fails %v: drift-a.json removed without --check-drift", failShow)
		}
	}
}

func TestRemoveAllTriesEveryPath(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory cannot be removed with os.Remove.
	bad := filepath.Join(dir, "bad")
	if err := os.MkdirAll(filepath.Join(bad, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	later := filepath.Join(dir, "later.json")
	if err := os.WriteFile(later, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeAll(nil, bad, later); err == nil {
		t.Error("no error for the path that could not be removed")
	}
	if fileExists(later) {
		t.Error("stopped at the first error: later path kept")
	}
}
