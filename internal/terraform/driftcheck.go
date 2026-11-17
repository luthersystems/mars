package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// DriftCheckCmd checks a plan for resource drift: resources whose real state
// changed outside Terraform since the last apply (the plan's
// resource_drift). It is the general part of sandbox-infrastructure-template
// tf/drift-check.sh (sandbox-infrastructure-template#114), and works on any
// Terraform project.
//
// What it keeps from the script, because it holds for any plan:
//
//   - The false-positive filter: an entry whose before and after differ only
//     by null versus an empty value ([], {}, "", 0, false) is not drift. AWS
//     and GCP APIs normalize these back and forth.
//   - The actionable join (template#102): a drifted resource is actionable
//     when the same plan has a change for it other than no-op or read, so
//     Terraform is about to act on it. Drift in computed attributes only
//     (a no-op change) is reported but not actionable. A change with no
//     actions counts as actionable, which fails safe.
//   - The action enrichment (template#105): each drifted resource gets the
//     actions of its resource change, or null.
//   - --ignore-drift (force apply) and --strict (alarm on any drift).
//   - apply_skipped: true when there is actionable drift and no
//     --ignore-drift, the field an apply wrapper reads to skip apply.
//
// What stays in the template's shell: the drift.json and drift-<stage>.json
// files under $MARS_PROJECT_ROOT/outputs (an Argo artifact path), the
// template_version and presets_version fields and log lines, and the
// script's choice to exit 0 on drift (template#108). The template writes the
// report with --out and maps exit code 2 as it needs.
//
// The report is JSON on stdout, or in the --out file. A human summary goes
// to stderr.
//
// Exit codes:
//
//	0  no drift; drift that is not actionable (without --strict); or any
//	   drift with --ignore-drift
//	2  actionable drift; with --strict, any drift
//	1  error: no plan file, terraform show failed, or a plan that is not
//	   terraform's JSON
type DriftCheckCmd struct {
	PlanFile    string `arg:"" name:"plan-file" help:"Saved plan file, or a plan rendered by terraform show -json."`
	IgnoreDrift bool   `name:"ignore-drift" help:"Force apply: report drift, set apply_skipped=false and exit 0."`
	Strict      bool   `name:"strict" help:"Exit 2 on any drift, actionable or not (refresh-only alarm)."`
	Out         string `name:"out" help:"Write the JSON report to this file instead of stdout."`
}

// DriftReport is drift-check's JSON report. Field order is fixed.
type DriftReport struct {
	DriftDetected   bool `json:"drift_detected"`
	DriftCount      int  `json:"drift_count"`
	Actionable      bool `json:"actionable"`
	ActionableCount int  `json:"actionable_count"`
	ApplySkipped    bool `json:"apply_skipped"`
	// Resources are the plan's resource_drift entries, unchanged, each with
	// "action": its resource change's actions, or null.
	Resources []map[string]any `json:"resources"`
}

const driftCheckName = "drift-check"

func (c *DriftCheckCmd) Run(ctx context.Context, rt *app.Runtime) error {
	raw, err := loadPlanJSON(ctx, rt, driftCheckName, c.PlanFile)
	if err != nil {
		return err
	}
	report, err := buildDriftReport(raw, c.IgnoreDrift)
	if err != nil {
		return cmdError(rt.Stderr, driftCheckName, "%v", err)
	}
	printDriftSummary(rt.Stderr, report)
	if err := writeReport(rt, driftCheckName, c.Out, report); err != nil {
		return err
	}
	return runner.ExitSilently(driftExitCode(report, c.IgnoreDrift, c.Strict))
}

func driftExitCode(r *DriftReport, ignoreDrift, strict bool) int {
	switch {
	case !r.DriftDetected, ignoreDrift:
		return exitOK
	case strict, r.Actionable:
		return exitDrift
	default:
		return exitOK
	}
}

// driftEntry is a resource_drift entry: the raw object for the report and
// the fields the check reads.
type driftEntry struct {
	obj map[string]any
	key objectKey
}

// objectKey names one resource object: its address, and its deposed key for
// a deposed object ("" for the current one).
type objectKey struct {
	address string
	deposed string
}

func buildDriftReport(raw []byte, ignoreDrift bool) (*DriftReport, error) {
	plan, err := parsePlan(raw)
	if err != nil {
		return nil, err
	}
	entries, err := plan.driftEntries()
	if err != nil {
		return nil, err
	}
	var drift []driftEntry
	for i, rawEntry := range entries {
		e, keep, err := readDriftEntry(rawEntry)
		if err != nil {
			return nil, fmt.Errorf("resource_drift[%d]: %w", i, err)
		}
		if keep {
			drift = append(drift, e)
		}
	}

	// Changes are keyed by (address, deposed), so a deposed object's change
	// (a create_before_destroy leftover, usually ["delete"]) neither makes
	// the current object's drift actionable nor replaces its action.
	actionable := map[objectKey]bool{}
	actionsByObj := map[objectKey]tfjson.Actions{}
	for _, rc := range plan.ResourceChanges {
		k := objectKey{rc.Address, rc.DeposedKey}
		acts := changeActions(rc)
		if !noOpOrRead(acts) {
			actionable[k] = true
		}
		// A later change of the same object wins, as in the script.
		actionsByObj[k] = acts
	}

	report := &DriftReport{Resources: []map[string]any{}}
	for _, e := range drift {
		// Drift of a deposed object is reported but never actionable:
		// Terraform only ever deletes a deposed object.
		if e.key.deposed == "" && actionable[e.key] {
			report.ActionableCount++
		}
		// e.obj is this entry's own decoded copy. A missing change or
		// actions is a nil Actions, which encodes as null.
		e.obj["action"] = actionsByObj[e.key]
		report.Resources = append(report.Resources, e.obj)
	}
	report.DriftCount = len(drift)
	report.DriftDetected = report.DriftCount > 0
	report.Actionable = report.ActionableCount > 0
	report.ApplySkipped = report.Actionable && !ignoreDrift
	return report, nil
}

// readDriftEntry decodes one resource_drift entry and reports whether it is
// real drift. An entry with no change is kept; one whose before and after
// are equal once normalized is a false positive. Terraform's before and
// after of a resource are objects (or null); anything else is an error.
func readDriftEntry(raw json.RawMessage) (driftEntry, bool, error) {
	v, err := decodeValue(raw)
	if err != nil {
		return driftEntry{}, false, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return driftEntry{}, false, fmt.Errorf("not an object")
	}
	addr, ok := obj["address"].(string)
	if !ok {
		return driftEntry{}, false, fmt.Errorf("address is not a string")
	}
	deposed, _ := obj["deposed"].(string)
	e := driftEntry{obj: obj, key: objectKey{addr, deposed}}
	change, present := obj["change"]
	if !present || change == nil {
		return e, true, nil
	}
	ch, ok := change.(map[string]any)
	if !ok {
		return driftEntry{}, false, fmt.Errorf("%s: change is not an object", addr)
	}
	before, err := sideObject(ch, "before")
	if err != nil {
		return driftEntry{}, false, fmt.Errorf("%s: %w", addr, err)
	}
	after, err := sideObject(ch, "after")
	if err != nil {
		return driftEntry{}, false, fmt.Errorf("%s: %w", addr, err)
	}
	return e, !jsonEqual(normalizeEmpty(before), normalizeEmpty(after)), nil
}

// sideObject is change.before or change.after, with null as {}.
func sideObject(ch map[string]any, side string) (map[string]any, error) {
	v := ch[side]
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("change.%s is not an object", side)
	}
	return m, nil
}

// normalizeEmpty maps null, [], {}, false, 0 and "" to null, recursively,
// so a null-versus-empty diff from a provider's API normalization is not
// drift.
func normalizeEmpty(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool:
		if !x {
			return nil
		}
		return x
	case json.Number:
		if isZeroNumber(x) {
			return nil
		}
		return x
	case string:
		if x == "" {
			return nil
		}
		return x
	case []any:
		if len(x) == 0 {
			return nil
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeEmpty(e)
		}
		return out
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeEmpty(e)
		}
		return out
	default:
		return v
	}
}

// printDriftSummary prints, per drifted resource, its address and each
// attribute whose normalized value changed, as `key: before -> after`.
func printDriftSummary(w io.Writer, r *DriftReport) {
	if !r.DriftDetected {
		fmt.Fprintln(w, "No resource drift detected.")
		return
	}
	fmt.Fprintf(w, "Drift detected: %d resource(s) have drifted.\n\n", r.DriftCount)
	for _, res := range r.Resources {
		fmt.Fprintf(w, "  %s\n", res["address"])
		ch, _ := res["change"].(map[string]any)
		before, _ := sideObject(ch, "before")
		after, _ := sideObject(ch, "after")
		keys := map[string]bool{}
		for k := range before {
			keys[k] = true
		}
		for k := range after {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			if jsonEqual(normalizeEmpty(before[k]), normalizeEmpty(after[k])) {
				continue
			}
			fmt.Fprintf(w, "    %s: %s -> %s\n", k, compactJSON(before[k]), compactJSON(after[k]))
		}
		fmt.Fprintln(w)
	}
	switch {
	case r.Actionable:
		fmt.Fprintf(w, "%d drifted resource(s) have changes in this plan (actionable); apply_skipped=%t.\n", r.ActionableCount, r.ApplySkipped)
	default:
		fmt.Fprintln(w, "No drifted resource has a change in this plan (not actionable).")
	}
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
