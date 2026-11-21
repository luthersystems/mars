package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/luthersystems/insideout-terraform-presets/pkg/planredact"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// PlanAllCmd plans every stage of the project's stages config in order and
// writes one plan-summary of them all. It is the general part of
// sandbox-infrastructure-template tf/plan-all.sh
// (sandbox-infrastructure-template#114); docs/stages.md has the config and
// the files it writes.
//
// Exit codes: 0 every stage planned (changes are in the summary, not the
// exit code); 1 a stage failed to plan, a new project's apply failed, or the
// config is bad.
type PlanAllCmd struct {
	Config     string   `name:"config" help:"Stages config file (default mars-stages.yaml)."`
	Stages     []string `name:"stages" help:"Plan only these stages, in this order (comma separated)."`
	Continue   bool     `name:"continue" help:"Continue an earlier plan-all: keep the summary rows and plan JSON of the stages this run does not plan."`
	CheckDrift bool     `name:"check-drift" help:"Also write each stage's drift report (it does not change the exit code)."`
	// PlanFile names the saved plan in every stage directory. A caller
	// that applies a plan later by name needs it: ui-core plans with
	// -out={planID}.tfplan in TF_CLI_ARGS_plan and applies {planID}.tfplan
	// in a later step, but mars's own -out comes after TF_CLI_ARGS_plan
	// and wins, so without this flag the plan would be saved under the
	// config's plan_file and the later apply would not find it
	// (sandbox-infrastructure-template#114).
	PlanFile string `name:"plan-file" help:"Saved plan file name in each stage directory (default: the config's plan_file)."`
}

// PlanStageCmd plans one stage: the general part of the template's
// tf/plan.sh and, with --refresh-only --check-drift --strict, of
// tf/drift-refresh.sh.
//
// Exit codes: 0 planned; 2 with --check-drift, actionable drift (any drift
// with --strict) and no --ignore-drift; 1 error.
type PlanStageCmd struct {
	Stage       string `arg:"" name:"stage" help:"Stage name from the stages config."`
	Config      string `name:"config" help:"Stages config file (default mars-stages.yaml)."`
	PlanFile    string `name:"plan-file" help:"Saved plan file name in the stage directory (default: the config's plan_file)."`
	RefreshOnly bool   `name:"refresh-only" help:"Plan with -refresh-only."`
	CheckDrift  bool   `name:"check-drift" help:"Write the drift report and exit as drift-check does."`
	IgnoreDrift bool   `name:"ignore-drift" help:"With --check-drift: report drift but exit 0 (apply_skipped=false)."`
	Strict      bool   `name:"strict" help:"With --check-drift: exit 2 on any drift."`
}

const (
	planAllName   = "plan-all"
	planStageName = "plan-stage"
)

// stagePlan is the result of planning one stage.
type stagePlan struct {
	raw     []byte
	plan    *planDoc
	summary *PlanSummary
}

// planOpts are planOneStage's options.
type planOpts struct {
	refreshOnly bool
	// drift: this run writes the stage's drift report, so a failure
	// removes it.
	drift bool
	// planned, when set, runs once terraform plan succeeded.
	planned func()
	// hook, when set, runs last in the stage directory.
	hook func(*stagePlan) error
}

// planOneStage plans a stage, writes its plan JSON (and stage summary), and
// in hook runs anything else that needs the stage directory. When any step
// fails, the stage's reports are removed (plan JSON, stage summary, and the
// drift report when this run writes one), so a failed stage never has them.
func (r *stageRun) planOneStage(ctx context.Context, st *StageEntry, planFile string, o planOpts) (*stagePlan, error) {
	var res *stagePlan
	planJSON := r.cfg.outPath(r.cfg.planJSONName(), st.Name)
	stageSummary := r.cfg.outPath(r.cfg.stageSummaryName(), st.Name)
	err := r.inStage(ctx, st, func() error {
		if err := r.plan(ctx, st, planFile, o.refreshOnly); err != nil {
			return err
		}
		if o.planned != nil {
			o.planned()
		}
		raw, plan, err := r.showJSON(ctx, st, planFile)
		if err != nil {
			return err
		}
		res = &stagePlan{raw: raw, plan: plan, summary: summarizePlan(plan)}
		// The plan JSON is uploaded with the outputs and served by ui-core
		// and reliable, and `terraform show -json` writes every input
		// variable value into it, the credential tfvars among them. Write
		// the redacted form; the summary and drift report use the exact
		// plan in res (luthersystems/reliable#2553). The tfplan.json alias
		// is a copy of this file, so it is redacted too.
		if planJSON != "" {
			redacted, err := planredact.JSON(raw)
			if err != nil {
				return fmt.Errorf("stage %s: %w", st.Name, err)
			}
			if err := writeReportFile(planJSON, redacted); err != nil {
				return err
			}
			r.logf("Plan JSON written to %s", planJSON)
		}
		if err := writeReportJSON(stageSummary, res.summary); err != nil {
			return err
		}
		if o.hook != nil {
			return o.hook(res)
		}
		return nil
	})
	if err != nil {
		return nil, removeAll(err, r.cfg.stageReports(st, o.drift)...)
	}
	return res, nil
}

// summaryRow is a stage's row in plan-all's summary: the template's fields.
func summaryRow(s *PlanSummary) orderedObject {
	return orderedObject{
		{"add", s.Add},
		{"change", s.Change},
		{"destroy", s.Destroy},
		{"import", s.Import},
		{"has_changes", s.HasChanges},
	}
}

var errorRow = orderedObject{{"error", true}}

// rowCounts are what the totals read from a summary row.
type rowCounts struct {
	isError, hasChanges           bool
	add, change, destroy, imports int
}

func countsOf(s *PlanSummary) rowCounts {
	return rowCounts{
		hasChanges: s.HasChanges,
		add:        s.Add, change: s.Change, destroy: s.Destroy, imports: s.Import,
	}
}

// readRow reads a row an earlier run wrote (a missing count is 0).
func readRow(raw json.RawMessage) (rowCounts, error) {
	v, err := decodeValue(raw)
	if err != nil {
		return rowCounts{}, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return rowCounts{}, errors.New("not an object")
	}
	num := func(k string) int {
		n, ok := m[k].(json.Number)
		if !ok {
			return 0
		}
		if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			return int(i)
		}
		f, _ := strconv.ParseFloat(string(n), 64)
		return int(f)
	}
	return rowCounts{
		isError:    m["error"] == true,
		hasChanges: m["has_changes"] == true,
		add:        num("add"), change: num("change"), destroy: num("destroy"), imports: num("import"),
	}, nil
}

// namedRow is a stage's summary row: body is written as is, counts feed the
// totals.
type namedRow struct {
	name   string
	body   any
	counts rowCounts
}

// cleanStale removes the reports an earlier run left. Only the files the
// config names are removed: the per-stage reports (plan JSON, stage summary,
// drift) of every configured stage, or with --continue of this run's stages
// only, so the other stages' stay; the plan alias; the summary without
// --continue; and the drift alias with --check-drift.
func (c *PlanAllCmd) cleanStale(cfg *StagesConfig, stages []*StageEntry) error {
	clean := stages
	var paths []string
	if !c.Continue {
		clean = nil
		for i := range cfg.Stages {
			clean = append(clean, &cfg.Stages[i])
		}
		paths = append(paths, cfg.outPath(cfg.summaryName(), ""))
	}
	for _, st := range clean {
		paths = append(paths, cfg.stageReports(st, true)...)
	}
	// The drift alias only when this run writes it.
	if c.CheckDrift {
		paths = append(paths, cfg.outPath(cfg.driftAliasName(), ""))
	}
	paths = append(paths, cfg.outPath(cfg.planAliasName(), ""))
	for _, p := range paths {
		if err := removeFile(p); err != nil {
			return err
		}
	}
	return nil
}

func (c *PlanAllCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := newStageRun(rt, planAllName, c.Config)
	if err != nil {
		return err
	}
	stages, err := r.cfg.selectStages(c.Stages)
	if err != nil {
		return r.fail("%v", err)
	}
	cfg := r.cfg
	inRun := map[string]bool{}
	for _, st := range stages {
		inRun[st.Name] = true
	}

	// With --continue, keep the rows of the other stages; an earlier
	// failed stage still fails this run, as in plan-all.sh.
	var rows []namedRow
	hadError := false
	if c.Continue {
		rows, err = readPriorRows(cfg.outPath(cfg.summaryName(), ""), inRun)
		if err != nil {
			return r.fail("%v", err)
		}
		for _, row := range rows {
			if row.counts.isError {
				hadError = true
			}
		}
	}
	planFile := c.PlanFile
	if planFile == "" {
		planFile = cfg.planFile()
	}
	// A bare name only: a name in a subdirectory that does not exist would
	// fail in every stage's terraform plan, after cleanStale has removed the
	// earlier run's reports.
	if !isPlainLocal(planFile) || filepath.Base(planFile) != planFile {
		return r.fail("--plan-file %q must be a file name inside the stage directory", planFile)
	}
	if err := c.cleanStale(cfg, stages); err != nil {
		return r.fail("%v", err)
	}

	for _, st := range stages {
		r.logf("\n=== Planning stage: %s ===\n", st.Name)
		applyFailed := false
		res, err := r.planOneStage(ctx, st, planFile, planOpts{drift: c.CheckDrift, hook: func(res *stagePlan) error {
			if c.CheckDrift {
				rep, err := buildDriftReport(res.raw, false)
				if err != nil {
					return fmt.Errorf("stage %s: %w", st.Name, err)
				}
				if err := r.writeDrift(rep, cfg.outPath(cfg.driftName(), st.Name)); err != nil {
					return err
				}
			}
			if !st.ApplyIfNoState {
				return nil
			}
			empty, err := r.stateIsEmpty(ctx, st)
			if err != nil {
				return err
			}
			if !empty {
				return nil
			}
			r.logf("\nINFO: New project detected (no managed resources in %s state).", st.Name)
			r.logf("INFO: Applying %s to create remote state for later stages...\n", st.Name)
			// Flags such as -out in the caller's TF_CLI_ARGS_plan or
			// TF_CLI_ARGS_apply are not valid for this apply.
			env := map[string]string{"TF_CLI_ARGS_plan": "", "TF_CLI_ARGS_apply": ""}
			if err := r.applyPlan(ctx, st, planFile, env); err != nil {
				r.logf("ERROR: %v", err)
				applyFailed = true
				return nil
			}
			r.logf("INFO: %s applied successfully.", st.Name)
			return nil
		}})
		if err != nil {
			r.logf("ERROR: %v", err)
			hadError = true
			rows = append(rows, namedRow{name: st.Name, body: errorRow, counts: rowCounts{isError: true}})
			if !cfg.continueOnError() {
				break
			}
			continue
		}
		row := summaryRow(res.summary)
		rows = append(rows, namedRow{name: st.Name, body: row, counts: countsOf(res.summary)})
		if b, err := marshalNoEscape(row); err == nil {
			r.logf("Stage %s: %s", st.Name, b)
		}
		if applyFailed {
			hadError = true
			if !cfg.continueOnError() {
				break
			}
		}
	}

	summary := buildAllSummary(rows)
	if p := cfg.outPath(cfg.summaryName(), ""); p != "" {
		if err := writeReportJSON(p, summary); err != nil {
			return r.fail("%v", err)
		}
		r.logf("\nPlan summary written to %s", p)
	}
	if b, err := encodeJSON(summary, true); err == nil {
		_, _ = rt.Stdout.Write(b)
	}

	if err := r.writePlanAlias(rows); err != nil {
		return r.fail("%v", err)
	}
	if c.CheckDrift {
		if err := r.writeDriftAlias(hadError); err != nil {
			return r.fail("%v", err)
		}
	}
	if cfg.StripProviders {
		r.logf("Stripping .terraform/providers under %s", cfg.root)
		if err := stripProviders(cfg.root, r.logf); err != nil {
			return r.fail("%v", err)
		}
	}
	if hadError {
		return runner.ExitSilently(exitError)
	}
	return nil
}

// writeDriftAlias writes plan-all's drift alias: the drift reports of every
// configured stage on disk, combined in config order. This run's stages'
// reports are fresh; with --continue, earlier steps' stay. When a stage
// failed or was not planned (hadError) it writes none and removes an old
// one, so no alias reads as clean for a run that did not finish.
func (r *stageRun) writeDriftAlias(hadError bool) error {
	cfg := r.cfg
	alias := cfg.outPath(cfg.driftAliasName(), "")
	if alias == "" {
		return nil
	}
	if hadError {
		r.logf("WARNING: a stage failed; %s not written", alias)
		return removeFile(alias)
	}
	var reps []*DriftReport
	for i := range cfg.Stages {
		p := cfg.outPath(cfg.driftName(), cfg.Stages[i].Name)
		if p == "" || !fileExists(p) {
			continue
		}
		rep, err := readDriftReport(p)
		if err != nil {
			return err
		}
		reps = append(reps, rep)
	}
	return r.writeDrift(combineDrift(reps), alias)
}

// readPriorRows reads the rows of an earlier summary, sorted by stage name,
// leaving out the stages of this run. A missing summary has none.
func readPriorRows(path string, inRun map[string]bool) ([]namedRow, error) {
	if path == "" || !fileExists(path) {
		return nil, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the summary an earlier run wrote
	if err != nil {
		return nil, err
	}
	var doc struct {
		Stages map[string]json.RawMessage `json:"stages"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("read the earlier summary %s: %w", path, err)
	}
	names := make([]string, 0, len(doc.Stages))
	for n := range doc.Stages {
		if !inRun[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]namedRow, 0, len(names))
	for _, n := range names {
		rc, err := readRow(doc.Stages[n])
		if err != nil {
			return nil, fmt.Errorf("the earlier summary's row of %s: %w", n, err)
		}
		out = append(out, namedRow{name: n, body: doc.Stages[n], counts: rc})
	}
	return out, nil
}

// buildAllSummary is plan-all's summary: the rows, the totals of the rows
// that are not errors, and has_changes.
func buildAllSummary(rows []namedRow) orderedObject {
	stages := orderedObject{}
	var add, change, destroy, imports int
	hasChanges := false
	for _, row := range rows {
		rc := row.counts
		if !rc.isError {
			add += rc.add
			change += rc.change
			destroy += rc.destroy
			imports += rc.imports
		}
		if rc.hasChanges {
			hasChanges = true
		}
		stages = append(stages, jsonField{row.name, row.body})
	}
	return orderedObject{
		{"stages", stages},
		{"total", orderedObject{{"add", add}, {"change", change}, {"destroy", destroy}, {"import", imports}}},
		{"has_changes", hasChanges},
	}
}

// writePlanAlias copies the preferred stage's plan JSON to the alias, or the
// last row's stage with a plan JSON on disk.
func (r *stageRun) writePlanAlias(rows []namedRow) error {
	cfg := r.cfg
	alias := cfg.outPath(cfg.planAliasName(), "")
	if alias == "" || cfg.planJSONName() == "" {
		return nil
	}
	src := ""
	for i := range cfg.Stages {
		if cfg.Stages[i].PlanAliasPreferred {
			if p := cfg.outPath(cfg.planJSONName(), cfg.Stages[i].Name); fileExists(p) {
				src = p
			}
		}
	}
	if src == "" {
		for _, row := range rows {
			if p := cfg.outPath(cfg.planJSONName(), row.name); fileExists(p) {
				src = p
			}
		}
	}
	if src == "" {
		r.logf("WARNING: no stage has plan JSON; %s not written", alias)
		return nil
	}
	if err := copyPlanJSON(src, alias); err != nil {
		return err
	}
	r.logf("Canonical %s written from %s", alias, src)
	return nil
}

func (c *PlanStageCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := newStageRun(rt, planStageName, c.Config)
	if err != nil {
		return err
	}
	st := r.cfg.stage(c.Stage)
	if st == nil {
		return r.fail("no stage %q in the stages config", c.Stage)
	}
	planFile := c.PlanFile
	if planFile == "" {
		planFile = r.cfg.planFile()
	}
	if !isPlainLocal(planFile) {
		return r.fail("--plan-file %q must be a file name inside the stage directory", planFile)
	}
	cfg := r.cfg
	// The stage's reports from an earlier run are stale (its drift report
	// only when this run writes one).
	if err := removeAll(nil, cfg.stageReports(st, c.CheckDrift)...); err != nil {
		return r.fail("%v", err)
	}
	// Once the plan succeeded, the aliases are this run's to write, and fail
	// removes them with the stage's reports, so a failed run leaves no
	// report or alias that looks like its result. A run that fails to plan
	// leaves the aliases as they were.
	var aliases []string
	planned := func() {
		aliases = append(aliases, cfg.outPath(cfg.planAliasName(), ""))
		if c.CheckDrift {
			aliases = append(aliases, cfg.outPath(cfg.driftAliasName(), st.Name))
		}
	}
	fail := func(err error) error {
		return r.fail("%v", removeAll(err, append(cfg.stageReports(st, c.CheckDrift), aliases...)...))
	}
	res, err := r.planOneStage(ctx, st, planFile, planOpts{refreshOnly: c.RefreshOnly, drift: c.CheckDrift, planned: planned})
	if err != nil {
		return fail(err)
	}
	if src := cfg.outPath(cfg.planJSONName(), st.Name); src != "" {
		if alias := cfg.outPath(cfg.planAliasName(), ""); alias != "" {
			if err := copyPlanJSON(src, alias); err != nil {
				return fail(err)
			}
			r.logf("Canonical %s written", alias)
		}
	}
	if !c.CheckDrift {
		return nil
	}
	rep, err := buildDriftReport(res.raw, c.IgnoreDrift)
	if err != nil {
		return fail(fmt.Errorf("stage %s: %w", st.Name, err))
	}
	printDriftSummary(rt.Stderr, rep)
	if err := r.writeDrift(rep, cfg.outPath(cfg.driftName(), st.Name), cfg.outPath(cfg.driftAliasName(), st.Name)); err != nil {
		return fail(err)
	}
	return runner.ExitSilently(driftExitCode(rep, c.IgnoreDrift, c.Strict))
}
