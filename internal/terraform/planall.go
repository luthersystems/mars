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
// in hook runs anything else that needs the stage directory.
func (r *stageRun) planOneStage(ctx context.Context, st *StageEntry, planFile string, refreshOnly bool, hook func(*stagePlan) error) (*stagePlan, error) {
	var res *stagePlan
	err := r.inStage(ctx, st, func() error {
		if err := r.plan(ctx, st, planFile, refreshOnly); err != nil {
			return err
		}
		raw, plan, err := r.showJSON(ctx, st, planFile)
		if err != nil {
			return err
		}
		res = &stagePlan{raw: raw, plan: plan, summary: summarizePlan(plan)}
		if p := r.cfg.outPath(r.cfg.planJSONName(), st.Name); p != "" {
			if err := writeReportFile(p, raw); err != nil {
				return err
			}
			r.logf("Plan JSON written to %s", p)
		}
		if err := writeReportJSON(r.cfg.outPath(r.cfg.stageSummaryName(), st.Name), res.summary); err != nil {
			return err
		}
		if hook != nil {
			return hook(res)
		}
		return nil
	})
	return res, err
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

// rowCounts reads a summary row: whether it is an error row, has changes,
// and its counts (a missing count is 0).
type rowCounts struct {
	isError, hasChanges           bool
	add, change, destroy, imports int
}

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

type namedRow struct {
	name string
	raw  json.RawMessage
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

	// Clear stale reports, or with --continue keep the other stages'.
	var prior []namedRow
	if c.Continue {
		for _, st := range stages {
			if err := removeFile(cfg.outPath(cfg.planJSONName(), st.Name)); err != nil {
				return r.fail("%v", err)
			}
			if err := removeFile(cfg.outPath(cfg.stageSummaryName(), st.Name)); err != nil {
				return r.fail("%v", err)
			}
		}
		if err := removeFile(cfg.outPath(cfg.planAliasName(), "")); err != nil {
			return r.fail("%v", err)
		}
		prior, err = readPriorRows(cfg.outPath(cfg.summaryName(), ""), inRun)
		if err != nil {
			return r.fail("%v", err)
		}
	} else {
		for _, g := range []string{cfg.outGlob(cfg.planJSONName()), cfg.outGlob(cfg.stageSummaryName())} {
			if err := removeGlob(g); err != nil {
				return r.fail("%v", err)
			}
		}
		for _, p := range []string{cfg.outPath(cfg.planAliasName(), ""), cfg.outPath(cfg.summaryName(), "")} {
			if err := removeFile(p); err != nil {
				return r.fail("%v", err)
			}
		}
	}

	hadError := false
	rows := append([]namedRow(nil), prior...)
	planFile := cfg.planFile()
	for _, st := range stages {
		r.logf("\n=== Planning stage: %s ===\n", st.Name)
		applyFailed := false
		res, err := r.planOneStage(ctx, st, planFile, false, func(res *stagePlan) error {
			if c.CheckDrift {
				rep, err := buildDriftReport(res.raw, false)
				if err != nil {
					return fmt.Errorf("stage %s: %w", st.Name, err)
				}
				if err := writeReportJSON(cfg.outPath(cfg.driftName(), st.Name), r.driftDoc(rep)); err != nil {
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
		})
		if err != nil {
			r.logf("ERROR: %v", err)
			hadError = true
			rows = append(rows, namedRow{st.Name, mustJSON(errorRow)})
			if !cfg.continueOnError() {
				break
			}
			continue
		}
		row := summaryRow(res.summary)
		rows = append(rows, namedRow{st.Name, mustJSON(row)})
		b, _ := marshalNoEscape(row)
		r.logf("Stage %s: %s", st.Name, b)
		if applyFailed {
			hadError = true
			if !cfg.continueOnError() {
				break
			}
		}
	}

	summary, err := buildAllSummary(rows)
	if err != nil {
		return r.fail("%v", err)
	}
	if p := cfg.outPath(cfg.summaryName(), ""); p != "" {
		if err := writeReportJSON(p, summary); err != nil {
			return r.fail("%v", err)
		}
		r.logf("\nPlan summary written to %s", p)
	}
	if b, err := indentedJSON(summary); err == nil {
		_, _ = rt.Stdout.Write(b)
	}

	if err := r.writePlanAlias(rows); err != nil {
		return r.fail("%v", err)
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

func mustJSON(v any) json.RawMessage {
	b, err := marshalNoEscape(v)
	if err != nil {
		panic(err)
	}
	return b
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
		out = append(out, namedRow{n, doc.Stages[n]})
	}
	return out, nil
}

// buildAllSummary is plan-all's summary: the rows, the totals of the rows
// that are not errors, and has_changes.
func buildAllSummary(rows []namedRow) (orderedObject, error) {
	stages := orderedObject{}
	var add, change, destroy, imports int
	hasChanges := false
	for _, row := range rows {
		rc, err := readRow(row.raw)
		if err != nil {
			return nil, fmt.Errorf("summary row of %s: %w", row.name, err)
		}
		if !rc.isError {
			add += rc.add
			change += rc.change
			destroy += rc.destroy
			imports += rc.imports
		}
		if rc.hasChanges {
			hasChanges = true
		}
		stages = append(stages, jsonField{row.name, row.raw})
	}
	return orderedObject{
		{"stages", stages},
		{"total", orderedObject{{"add", add}, {"change", change}, {"destroy", destroy}, {"import", imports}}},
		{"has_changes", hasChanges},
	}, nil
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
	res, err := r.planOneStage(ctx, st, planFile, c.RefreshOnly, nil)
	if err != nil {
		return r.fail("%v", err)
	}
	if src := r.cfg.outPath(r.cfg.planJSONName(), st.Name); src != "" {
		if alias := r.cfg.outPath(r.cfg.planAliasName(), ""); alias != "" {
			if err := copyFile(src, alias); err != nil {
				return r.fail("%v", err)
			}
			r.logf("Canonical %s written", alias)
		}
	}
	if !c.CheckDrift {
		return nil
	}
	rep, err := buildDriftReport(res.raw, c.IgnoreDrift)
	if err != nil {
		return r.fail("stage %s: %v", st.Name, err)
	}
	printDriftSummary(rt.Stderr, rep)
	if err := r.writeDrift(st, rep, true); err != nil {
		return r.fail("%v", err)
	}
	return runner.ExitSilently(driftExitCode(rep, c.IgnoreDrift, c.Strict))
}
