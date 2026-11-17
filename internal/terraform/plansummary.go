package terraform

import (
	"context"
	"slices"

	"github.com/luthersystems/mars/internal/app"
)

// PlanSummaryCmd counts the resource changes of a plan and lists their
// addresses, as JSON. It is the general part of
// sandbox-infrastructure-template tf/plan-all.sh's per-stage counts
// (sandbox-infrastructure-template#114). The template's multi-stage run, the
// new-project apply of cloud-provision, the aggregation of stages into
// outputs/plan-summary.json and the tfplan.json alias stay in its shell.
//
// add, change, destroy and import are the template's counts, which follow
// terraform's own "Plan: X to add, Y to change, Z to destroy" line: no-op
// and read changes are not counted, and a replacement counts once in add
// and once in destroy. replace counts replacements alone. import counts
// changes that import a resource: change.importing set (Terraform 1.5+), or
// an "import" action, which the template's fixtures use.
//
// addresses lists each changed resource once by kind: create, update,
// delete or replace. addresses.import lists the imported ones (an import may
// also be an update).
//
// Exit codes: 0 summary written, 1 error (no plan file, terraform show
// failed, or a plan that is not terraform's JSON).
type PlanSummaryCmd struct {
	PlanFile string `arg:"" name:"plan-file" help:"Saved plan file, or a plan rendered by terraform show -json."`
	Out      string `name:"out" help:"Write the JSON summary to this file instead of stdout."`
}

// PlanSummary is plan-summary's JSON report. Field order is fixed.
type PlanSummary struct {
	Add        int           `json:"add"`
	Change     int           `json:"change"`
	Destroy    int           `json:"destroy"`
	Replace    int           `json:"replace"`
	Import     int           `json:"import"`
	HasChanges bool          `json:"has_changes"`
	Addresses  PlanAddresses `json:"addresses"`
}

// PlanAddresses are the changed resources by kind, in plan order. Each list
// is [] when empty, never null.
type PlanAddresses struct {
	Create  []string `json:"create"`
	Update  []string `json:"update"`
	Delete  []string `json:"delete"`
	Replace []string `json:"replace"`
	Import  []string `json:"import"`
}

const planSummaryName = "plan-summary"

func (c *PlanSummaryCmd) Run(ctx context.Context, rt *app.Runtime) error {
	raw, err := loadPlanJSON(ctx, rt, planSummaryName, c.PlanFile)
	if err != nil {
		return err
	}
	plan, err := parsePlan(raw)
	if err != nil {
		return cmdError(rt.Stderr, planSummaryName, "%v", err)
	}
	return writeReport(rt, planSummaryName, c.Out, summarizePlan(plan))
}

func summarizePlan(plan *planDoc) *PlanSummary {
	s := &PlanSummary{Addresses: PlanAddresses{
		Create: []string{}, Update: []string{}, Delete: []string{}, Replace: []string{}, Import: []string{},
	}}
	for _, rc := range plan.ResourceChanges {
		acts := rc.actions()
		if rc.importing() || slices.Contains(acts, "import") {
			s.Import++
			s.Addresses.Import = append(s.Addresses.Import, rc.Address)
		}
		if isNoOpOrRead(acts) {
			continue
		}
		create := slices.Contains(acts, "create")
		update := slices.Contains(acts, "update")
		del := slices.Contains(acts, "delete")
		if create {
			s.Add++
		}
		if update {
			s.Change++
		}
		if del {
			s.Destroy++
		}
		switch {
		case create && del:
			s.Replace++
			s.Addresses.Replace = append(s.Addresses.Replace, rc.Address)
		case create:
			s.Addresses.Create = append(s.Addresses.Create, rc.Address)
		case update:
			s.Addresses.Update = append(s.Addresses.Update, rc.Address)
		case del:
			s.Addresses.Delete = append(s.Addresses.Delete, rc.Address)
		}
	}
	s.HasChanges = s.Add+s.Change+s.Destroy+s.Import > 0
	return s
}
