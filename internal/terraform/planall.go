package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

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

// planOneStage plans a stage, writes its plan JSON (and stage summary), and
// in hook runs anything else that needs the stage directory. When any step
// fails, every per-stage report of the stage is removed (plan JSON, stage
// summary, drift report), so a failed stage never has them.
func (r *stageRun) planOneStage(ctx context.Context, st *StageEntry, planFile string, refreshOnly bool, hook func(*stagePlan) error) (*stagePlan, error) {
	var res *stagePlan
	planJSON := r.cfg.outPath(r.cfg.planJSONName(), st.Name)
	stageSummary := r.cfg.outPath(r.cfg.stageSummaryName(), st.Name)
	err := r.inStage(ctx, st, func() error {
		if err := r.plan(ctx, st, planFile, refreshOnly); err != nil {
			return err
		}
		raw, plan, err := r.showJSON(ctx, st, planFile)
		if err != nil {
			return err
		}
		res = &stagePlan{raw: raw, plan: plan, summary: summarizePlan(plan)}
		if err := writeReportFile(planJSON, raw); err != nil {
			return err
		}
		if planJSON != "" {
			r.logf("Plan JSON written to %s", planJSON)
		}
		if err := writeReportJSON(stageSummary, res.summary); err != nil {
			return err
		}
		if hook != nil {
			return hook(res)
		}
		return nil
	})
	if err != nil {
		// Also the drift report a hook may have written.
		return nil, removeAll(err, r.stageReports(st)...)
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
	r := &stageRun{cfg: cfg}
	for _, st := range clean {
		paths = append(paths, r.stageReports(st)...)
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
	if err := c.cleanStale(cfg, stages); err != nil {
		return r.fail("%v", err)
	}

	planFile := cfg.planFile()
	var driftReps []*DriftReport
	for _, st := range stages {
		r.logf("\n=== Planning stage: %s ===\n", st.Name)
		applyFailed := false
		var stageDrift *DriftReport
		res, err := r.planOneStage(ctx, st, planFile, false, func(res *stagePlan) error {
			if c.CheckDrift {
				rep, err := buildDriftReport(res.raw, false)
				if err != nil {
					return fmt.Errorf("stage %s: %w", st.Name, err)
				}
				if err := r.writeDriftReport(cfg.outPath(cfg.driftName(), st.Name), rep); err != nil {
					return err
				}
				stageDrift = rep
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
		})
		if err != nil {
			r.logf("ERROR: %v", err)
			hadError = true
			rows = append(rows, namedRow{name: st.Name, body: errorRow, counts: rowCounts{isError: true}})
			if !cfg.continueOnError() {
				break
			}
			continue
		}
		if stageDrift != nil {
			driftReps = append(driftReps, stageDrift)
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
		// The drift alias is this run's planned stages' reports combined.
		if err := r.writeDriftReport(cfg.outPath(cfg.driftAliasName(), ""), combineDrift(driftReps)); err != nil {
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
	if err := copyFile(src, alias); err != nil {
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
	// The stage's reports from an earlier run are stale.
	if err := removeAll(nil, r.stageReports(st)...); err != nil {
		return r.fail("%v", err)
	}
	// Once the plan succeeded, fail removes the stage's reports and the
	// aliases this run writes, so a failed run leaves no report or alias
	// that looks like its result. A run that fails to plan leaves the
	// aliases as they were.
	var aliases []string
	fail := func(err error) error {
		return r.fail("%v", removeAll(err, append(r.stageReports(st), aliases...)...))
	}
	res, err := r.planOneStage(ctx, st, planFile, c.RefreshOnly, nil)
	if err != nil {
		return r.fail("%v", err)
	}
	if src := r.cfg.outPath(r.cfg.planJSONName(), st.Name); src != "" {
		if alias := r.cfg.outPath(r.cfg.planAliasName(), ""); alias != "" {
			aliases = append(aliases, alias)
			if err := copyFile(src, alias); err != nil {
				return fail(err)
			}
			r.logf("Canonical %s written", alias)
		}
	}
	if !c.CheckDrift {
		return nil
	}
	// From here the drift alias is this run's to write: a failure removes
	// it rather than leave an earlier run's.
	if alias := r.cfg.outPath(r.cfg.driftAliasName(), st.Name); alias != "" {
		aliases = append(aliases, alias)
	}
	rep, err := buildDriftReport(res.raw, c.IgnoreDrift)
	if err != nil {
		return fail(fmt.Errorf("stage %s: %w", st.Name, err))
	}
	printDriftSummary(rt.Stderr, rep)
	if err := r.writeDrift(st, rep, true); err != nil {
		return fail(err)
	}
	return runner.ExitSilently(driftExitCode(rep, c.IgnoreDrift, c.Strict))
}
