package terraform

import (
	"context"
	"path/filepath"

	"github.com/luthersystems/mars/internal/app"
)

// ApplyStageCmd applies one stage of the project's stages config: a saved
// plan (--plan), or a plan it saves first. It is the general part of
// sandbox-infrastructure-template tf/apply-plan.sh and the drift mode of
// tf/apply-with-outputs.sh (sandbox-infrastructure-template#114). The git
// snapshot and push of the template's infra repo stay in its shell.
//
// With --check-drift it writes the drift report first and, when the report
// has apply_skipped (actionable drift without --ignore-drift), does not
// apply and exits 0, as the template's apply wrappers do (template#108).
//
// Exit codes: 0 applied, or the apply skipped on drift; 1 error.
type ApplyStageCmd struct {
	Stage          string `arg:"" name:"stage" help:"Stage name from the stages config."`
	Config         string `name:"config" help:"Stages config file (default mars-stages.yaml)."`
	Plan           string `name:"plan" help:"Apply this saved plan file (in the stage directory) instead of planning."`
	CheckDrift     bool   `name:"check-drift" help:"Check the plan for drift first; skip the apply on actionable drift."`
	IgnoreDrift    bool   `name:"ignore-drift" help:"With --check-drift: apply despite drift (apply_skipped=false)."`
	Outputs        bool   `name:"outputs" help:"Write terraform output -json to the config's outputs files after the apply."`
	CleanProviders bool   `name:"clean-providers" help:"Delete the stage's .terraform/providers before terraform init."`
}

const applyStageName = "apply-stage"

func (c *ApplyStageCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := newStageRun(rt, applyStageName, c.Config)
	if err != nil {
		return err
	}
	cfg := r.cfg
	st := cfg.stage(c.Stage)
	if st == nil {
		return r.fail("no stage %q in the stages config", c.Stage)
	}
	planFile := c.Plan
	if planFile != "" && !isPlainLocal(planFile) {
		return r.fail("--plan %q must be a file name inside the stage directory", planFile)
	}
	if err := r.runSetup(ctx, st); err != nil {
		return r.fail("%v", err)
	}
	dir := cfg.stageDir(st)
	if planFile != "" && !fileExists(filepath.Join(dir, planFile)) {
		return r.fail("plan file not found: %s", filepath.Join(dir, planFile))
	}
	if c.CleanProviders {
		// Through an os.Root of the project: a .terraform that links out of
		// the project is refused, not cleaned.
		rel, err := filepath.Rel(cfg.root, filepath.Join(dir, ".terraform", "providers"))
		if err != nil {
			return r.fail("%v", err)
		}
		if err := removeAllInRoot(cfg.root, rel); err != nil {
			return r.fail("stage %s: clean providers: %v", st.Name, err)
		}
	}

	skipped := false
	err = r.enterStage(ctx, st, func() error {
		if planFile == "" {
			planFile = cfg.planFile()
			if err := r.plan(ctx, st, planFile, false); err != nil {
				return err
			}
		}
		if c.CheckDrift {
			raw, _, err := r.showJSON(ctx, st, planFile)
			if err != nil {
				return err
			}
			rep, err := buildDriftReport(raw, c.IgnoreDrift)
			if err != nil {
				return err
			}
			printDriftSummary(rt.Stderr, rep)
			if err := r.writeDrift(rep, cfg.outPath(cfg.driftName(), st.Name), cfg.outPath(cfg.driftAliasName(), st.Name)); err != nil {
				return err
			}
			if rep.ApplySkipped {
				r.logf("INFO: apply_skipped=true in the drift report; skipping terraform apply. Re-run with --ignore-drift to force.")
				skipped = true
				return nil
			}
		}
		if err := r.applyPlan(ctx, st, planFile, nil); err != nil {
			return err
		}
		if !c.Outputs {
			return nil
		}
		out, err := r.capture(ctx, st, "output", "-json")
		if err != nil {
			return err
		}
		for _, p := range []string{cfg.outPath(cfg.outputsName(), st.Name), cfg.outPath(cfg.outputsAliasName(), st.Name)} {
			if err := writeReportFile(p, out); err != nil {
				return err
			}
			if p != "" {
				r.logf("Outputs written to %s", p)
			}
		}
		return nil
	})
	if err != nil {
		return r.fail("%v", err)
	}
	if skipped || c.CheckDrift {
		return nil
	}
	// No drift check: an empty report, for readers that always want one.
	if alias := cfg.outPath(cfg.driftAliasName(), st.Name); alias != "" && !fileExists(alias) {
		if err := r.writeDrift(&DriftReport{Resources: []map[string]any{}}, cfg.outPath(cfg.driftAliasName(), st.Name)); err != nil {
			return r.fail("%v", err)
		}
	}
	return nil
}
