package terraform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/jqvalue"
	"github.com/luthersystems/mars/internal/runner"
)

// DriftCheckCmd checks a saved plan file for resource drift and writes a
// drift report. It is the Go port of sandbox-infrastructure-template
// tf/drift-check.sh (sandbox-infrastructure-template#114) and reproduces its
// stdout, stderr, exit codes and drift.json byte for byte, so the template
// can call `mars <env> drift-check` in place of the script. The defaults are
// the template's: ui-core reads drift.json from /marsproject/outputs, and
// reliable and ui-core read its fields (drift_detected, actionable,
// apply_skipped, template_version, presets_version, resources).
//
// Exit codes, as the script's:
//
//	0  no drift, or drift (apply_skipped in drift.json carries the gate,
//	   sandbox-infrastructure-template#108), or drift with --ignore-drift
//	1  error (missing plan file, unwritable outputs directory)
//	2  --strict and drift (the refresh-only alarm path)
//	5  a plan that is not JSON, or whose JSON does not have terraform's
//	   shape: jq 1.7's exit code, which the script passed through
//
// Any other code is terraform's own, from a failed `terraform show -json`.
type DriftCheckCmd struct {
	PlanFile    string `arg:"" name:"plan-file" help:"Saved terraform plan file."`
	IgnoreDrift bool   `name:"ignore-drift" help:"Force-apply override: write apply_skipped=false and exit 0 despite drift."`
	Stage       string `name:"stage" help:"Also write the report to drift-<stage>.json."`
	Strict      bool   `name:"strict" help:"Exit 2 when any drift is found (refresh-only alarm path)."`
	OutputsDir  string `name:"outputs-dir" help:"Directory for drift.json. Default: $MARS_PROJECT_ROOT/outputs, or /marsproject/outputs."`
	// The provenance fields of drift.json. The template exports these; empty
	// writes JSON null, which ui-core reads as unknown.
	TemplateVersion string `name:"template-version" env:"TEMPLATE_VERSION" help:"Template version recorded in drift.json."`
	PresetsVersion  string `name:"presets-version" env:"PRESETS_VERSION" help:"Presets version recorded in drift.json."`
}

// defaultProjectRoot is where the mars image mounts the project, and so the
// template's MARS_PROJECT_ROOT inside the container.
const defaultProjectRoot = "/marsproject"

func (c *DriftCheckCmd) Run(ctx context.Context, rt *app.Runtime) error {
	stdout, stderr := rt.Stdout, rt.Stderr
	st, err := os.Stat(c.PlanFile)
	if err != nil || !st.Mode().IsRegular() {
		fmt.Fprintf(stderr, "ERROR: plan file not found: %s\n", c.PlanFile)
		return runner.ExitSilently(1)
	}

	outputsDir := c.outputsDir()
	fmt.Fprintf(stdout, "template_version=%s\n", orUnknown(c.TemplateVersion))
	fmt.Fprintf(stdout, "presets_version=%s\n", orUnknown(c.PresetsVersion))

	// The script ran bare terraform from PATH with no workspace selection
	// (a plan file needs none), and printed nothing of its own, so this
	// does not go through s.sequence.
	raw, err := rt.Runner.Capture(ctx, runner.Command{
		Argv:   []string{"terraform", "show", "-json", c.PlanFile},
		Stderr: stderr,
		Quiet:  true,
	})
	if err != nil {
		var execErr *runner.ExitError
		if errors.As(err, &execErr) {
			return runner.ExitSilently(execErr.Code)
		}
		// bash's text for a missing command, with the script name in place
		// of the line prefix bash adds.
		fmt.Fprintf(stderr, "drift-check: terraform: command not found\n")
		return runner.ExitSilently(127)
	}
	return driftCheck(raw, c, outputsDir, stdout, stderr)
}

// outputsDir is --outputs-dir, else the script's $MARS_PROJECT_ROOT/outputs
// (joined as text, as the script did, so the logged path is the same).
func (c *DriftCheckCmd) outputsDir() string {
	if c.OutputsDir != "" {
		return c.OutputsDir
	}
	root := os.Getenv("MARS_PROJECT_ROOT")
	if root == "" {
		root = defaultProjectRoot
	}
	return root + "/outputs"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// shellText is what `x="$(cmd)"; echo "$x"` hands the next command: the
// output without trailing newlines, then one newline. jq's error location
// "(at <stdin>:N)" counts the newlines of that text.
func shellText(out []byte) string { return strings.TrimRight(string(out), "\n") + "\n" }

// jqFail prints a jq runtime error as jq does and returns jq's exit code 5.
func jqFail(stderr io.Writer, input string, err error) error {
	var jqErr *jqvalue.Error
	if !errors.As(err, &jqErr) {
		fmt.Fprintf(stderr, "jq: error: %v\n", err)
		return runner.ExitSilently(5)
	}
	fmt.Fprintf(stderr, "jq: error (at <stdin>:%d): %s\n", strings.Count(input, "\n"), jqErr.Message)
	return runner.ExitSilently(5)
}

func driftCheck(raw []byte, c *DriftCheckCmd, outputsDir string, stdout, stderr io.Writer) error {
	planText := shellText(raw)
	plan, err := jqvalue.Parse([]byte(planText))
	if errors.Is(err, jqvalue.ErrNoInput) {
		// jq ran no filter on empty input, so the script's drift count was
		// empty, which bash's -eq reads as 0.
		fmt.Fprintln(stdout, "No resource drift detected.")
		return nil
	}
	if err != nil {
		// jq 1.7 exits 5 on input it cannot parse; its message (with a
		// line and column) is not reproduced.
		fmt.Fprintf(stderr, "jq: parse error: %v\n", err)
		return runner.ExitSilently(5)
	}

	drift, err := filterDrift(plan)
	if err != nil {
		return jqFail(stderr, planText, err)
	}
	if len(drift) == 0 {
		fmt.Fprintln(stdout, "No resource drift detected.")
		return nil
	}

	actionable, err := actionableCount(plan, drift)
	if err != nil {
		return jqFail(stderr, planText, err)
	}
	actionsMap, err := addrToActions(plan)
	if err != nil {
		return jqFail(stderr, planText, err)
	}
	// The enrichment read the drift array as the script's jq printed it.
	driftText := shellText([]byte(jqvalue.Pretty(jqvalue.Arr(drift...))))
	enriched, err := enrichActions(drift, actionsMap)
	if err != nil {
		return jqFail(stderr, driftText, err)
	}

	fmt.Fprintf(stdout, "Drift detected: %d resource(s) have drifted.\n", len(drift))
	fmt.Fprintln(stdout)
	enrichedText := shellText([]byte(jqvalue.Pretty(jqvalue.Arr(enriched...))))
	if err := printDriftSummary(stdout, enriched); err != nil {
		return jqFail(stderr, enrichedText, err)
	}

	if err := os.MkdirAll(outputsDir, 0o755); err != nil { //nolint:gosec // G301: the outputs directory is an Argo artifact other steps read, as the script's mkdir -p made it
		fmt.Fprintf(stderr, "mkdir: cannot create directory '%s': %s\n", outputsDir, unwrapPathError(err))
		return runner.ExitSilently(1)
	}

	report := jqvalue.NewObject()
	report.Set("drift_detected", jqvalue.Bool(true))
	report.Set("drift_count", jqvalue.Int(len(drift)))
	report.Set("actionable", jqvalue.Bool(actionable > 0))
	report.Set("apply_skipped", jqvalue.Bool(!c.IgnoreDrift && actionable > 0))
	report.Set("template_version", strOrNull(c.TemplateVersion))
	report.Set("presets_version", strOrNull(c.PresetsVersion))
	report.Set("resources", jqvalue.Arr(enriched...))
	body := []byte(jqvalue.Pretty(report) + "\n")

	// drift.json is always written: the Argo artifact path expects it.
	driftPath := outputsDir + "/drift.json"
	if err := writeArtifact(stderr, driftPath, body); err != nil {
		return err
	}
	if c.Stage != "" {
		stagePath := outputsDir + "/drift-" + c.Stage + ".json"
		if err := writeArtifact(stderr, stagePath, body); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Drift report written to %s (and drift.json)\n", stagePath)
	} else {
		fmt.Fprintf(stdout, "Drift report written to %s\n", driftPath)
	}

	if c.IgnoreDrift {
		fmt.Fprintln(stdout, "WARNING: Drift ignored (--ignore-drift flag set); force-applying despite drift (apply_skipped=false).")
		return nil
	}
	if c.Strict {
		// The standalone refresh-only alarm path (drift-refresh.sh): any
		// drift fails the caller's workflow step visibly.
		return runner.ExitSilently(2)
	}
	if actionable > 0 {
		fmt.Fprintln(stdout, "INFO: actionable drift detected; apply_skipped=true. Apply wrappers will skip terraform apply. See reliable + UI for verdict.")
	} else {
		fmt.Fprintln(stdout, "INFO: drift detected but no drifted resource is being applied; not blocking apply.")
	}
	return nil
}

func strOrNull(s string) *jqvalue.Value {
	if s == "" {
		return jqvalue.NullV()
	}
	return jqvalue.Str(s)
}

// unwrapPathError gives the system error text as the shell tools print it
// (strerror: "Permission denied", not Go's "permission denied").
func unwrapPathError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	msg := err.Error()
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return msg
}

func writeArtifact(stderr io.Writer, path string, body []byte) error {
	if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec // G306: an Argo artifact other steps read, as the script's redirect made it
		fmt.Fprintf(stderr, "drift-check: %s: %s\n", path, unwrapPathError(err))
		return runner.ExitSilently(1)
	}
	return nil
}

// normalizeEmpty is the script's jq normalize_empty: null, [], {}, false, 0
// and "" all become null, recursively, so that a null-versus-empty diff from
// the provider's API normalization is not drift.
func normalizeEmpty(v *jqvalue.Value) *jqvalue.Value {
	switch v.Kind {
	case jqvalue.Null, jqvalue.False:
		return jqvalue.NullV()
	case jqvalue.Number:
		if v.IsZero() {
			return jqvalue.NullV()
		}
		return v
	case jqvalue.String:
		if v.Str == "" {
			return jqvalue.NullV()
		}
		return v
	case jqvalue.Array:
		if len(v.Elems) == 0 {
			return jqvalue.NullV()
		}
		out := jqvalue.Arr()
		for _, e := range v.Elems {
			out.Elems = append(out.Elems, normalizeEmpty(e))
		}
		return out
	case jqvalue.Object:
		if len(v.Keys) == 0 {
			return jqvalue.NullV()
		}
		out := jqvalue.NewObject()
		for _, k := range v.Keys {
			out.Set(k, normalizeEmpty(v.Members[k]))
		}
		return out
	default:
		return v
	}
}

// filterDrift keeps the resource_drift entries whose before and after still
// differ after normalizeEmpty. An entry with no change is kept.
func filterDrift(plan *jqvalue.Value) ([]*jqvalue.Value, error) {
	rd, err := jqvalue.Index(plan, "resource_drift")
	if err != nil {
		return nil, err
	}
	entries, err := jqvalue.Iterate(jqvalue.Alt(rd, jqvalue.Arr()))
	if err != nil {
		return nil, err
	}
	var out []*jqvalue.Value
	for _, entry := range entries {
		change, err := jqvalue.Index(entry, "change")
		if err != nil {
			return nil, err
		}
		if change.Kind == jqvalue.Null {
			out = append(out, entry)
			continue
		}
		before, err := jqvalue.Index(change, "before")
		if err != nil {
			return nil, err
		}
		after, err := jqvalue.Index(change, "after")
		if err != nil {
			return nil, err
		}
		nb := normalizeEmpty(jqvalue.Alt(before, jqvalue.NewObject()))
		na := normalizeEmpty(jqvalue.Alt(after, jqvalue.NewObject()))
		if !jqvalue.Equal(nb, na) {
			out = append(out, entry)
		}
	}
	return out, nil
}

var (
	noOpActions = jqvalue.Arr(jqvalue.Str("no-op"))
	readActions = jqvalue.Arr(jqvalue.Str("read"))
)

// resourceChanges is the script's (.resource_changes // [])[].
func resourceChanges(plan *jqvalue.Value) ([]*jqvalue.Value, error) {
	rc, err := jqvalue.Index(plan, "resource_changes")
	if err != nil {
		return nil, err
	}
	return jqvalue.Iterate(jqvalue.Alt(rc, jqvalue.Arr()))
}

// changeActions is .change.actions.
func changeActions(rc *jqvalue.Value) (*jqvalue.Value, error) {
	change, err := jqvalue.Index(rc, "change")
	if err != nil {
		return nil, err
	}
	return jqvalue.Index(change, "actions")
}

// actionableCount counts the drift entries whose address has a resource
// change other than no-op or read: drift Terraform is about to act on
// (sandbox-infrastructure-template#102). A change with no actions counts,
// which fails safe toward skipping apply.
func actionableCount(plan *jqvalue.Value, drift []*jqvalue.Value) (int, error) {
	changes, err := resourceChanges(plan)
	if err != nil {
		return 0, err
	}
	var addrs []*jqvalue.Value
	for _, rc := range changes {
		actions, err := changeActions(rc)
		if err != nil {
			return 0, err
		}
		if jqvalue.Equal(actions, noOpActions) || jqvalue.Equal(actions, readActions) {
			continue
		}
		addr, err := jqvalue.Index(rc, "address")
		if err != nil {
			return 0, err
		}
		addrs = append(addrs, addr)
	}
	// The script's jq took `unique` of these addresses. Sorting is skipped
	// here: order only matters to a run search for an array address, and an
	// array address always fails later, at enrichActions, as it did in jq.
	n := 0
	for _, entry := range drift {
		addr, err := jqvalue.Index(entry, "address")
		if err != nil {
			return 0, err
		}
		if jqIndexIn(addrs, addr) {
			n++
		}
	}
	return n, nil
}

// jqIndexIn is `$list | index($x)` used as a condition: jq's index on an
// array finds $x as an element, or, when $x is an array, as a run of
// elements (and an empty run is never found).
func jqIndexIn(list []*jqvalue.Value, x *jqvalue.Value) bool {
	if x.Kind != jqvalue.Array {
		for _, e := range list {
			if jqvalue.Equal(e, x) {
				return true
			}
		}
		return false
	}
	n := len(x.Elems)
	if n == 0 {
		return false
	}
	for i := 0; i+n <= len(list); i++ {
		match := true
		for j := 0; j < n; j++ {
			if !jqvalue.Equal(list[i+j], x.Elems[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// addrToActions maps each resource change's address to its actions (null
// when it has none); a later change of the same address wins.
func addrToActions(plan *jqvalue.Value) (*jqvalue.Value, error) {
	changes, err := resourceChanges(plan)
	if err != nil {
		return nil, err
	}
	m := jqvalue.NewObject()
	for _, rc := range changes {
		// jq's `.[k] = v` evaluates v first, so its errors come first.
		actions, err := changeActions(rc)
		if err != nil {
			return nil, err
		}
		addr, err := jqvalue.Index(rc, "address")
		if err != nil {
			return nil, err
		}
		if addr.Kind != jqvalue.String {
			return nil, &jqvalue.Error{Message: "Cannot index object with " + addr.Kind.TypeName()}
		}
		m.Set(addr.Str, jqvalue.Alt(actions, jqvalue.NullV()))
	}
	return m, nil
}

// enrichActions adds each drift entry's joined resource-change actions as
// "action" (sandbox-infrastructure-template#105); the presets drift
// classifier reads it.
func enrichActions(drift []*jqvalue.Value, actionsMap *jqvalue.Value) ([]*jqvalue.Value, error) {
	out := make([]*jqvalue.Value, 0, len(drift))
	for _, entry := range drift {
		addr, err := jqvalue.Index(entry, "address")
		if err != nil {
			return nil, err
		}
		action, err := jqvalue.IndexBy(actionsMap, addr)
		if err != nil {
			return nil, err
		}
		// entry + {action: ...}: null + object is the object.
		merged := jqvalue.NewObject()
		if entry.Kind == jqvalue.Object {
			for _, k := range entry.Keys {
				merged.Set(k, entry.Members[k])
			}
		}
		merged.Set("action", jqvalue.Alt(action, jqvalue.NullV()))
		out = append(out, merged)
	}
	return out, nil
}

// printDriftSummary prints, per drifted resource, its address and each
// before attribute whose normalized value changed, as `key: before ->
// after`. Lines go out as they are made, as jq -r streamed them, so a jq
// error leaves the same partial output.
func printDriftSummary(w io.Writer, drift []*jqvalue.Value) error {
	for _, entry := range drift {
		addr, err := jqvalue.Index(entry, "address")
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  %s\n", jqvalue.Interpolate(addr))
		change, err := jqvalue.Index(entry, "change")
		if err != nil {
			return err
		}
		before, err := sideEntries(change, "before")
		if err != nil {
			return err
		}
		after, err := sideEntries(change, "after")
		if err != nil {
			return err
		}
		var lines []string
		for _, b := range before {
			av := jqvalue.NullV()
			for _, a := range after {
				if jqvalue.Equal(a.Key, b.Key) {
					av = a.Value
					break
				}
			}
			if jqvalue.Equal(normalizeEmpty(b.Value), normalizeEmpty(av)) {
				continue
			}
			lines = append(lines, fmt.Sprintf("    %s: %s -> %s", jqvalue.Interpolate(b.Key), jqvalue.Compact(b.Value), jqvalue.Compact(av)))
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func sideEntries(change *jqvalue.Value, side string) ([]jqvalue.Entry, error) {
	v, err := jqvalue.Index(change, side)
	if err != nil {
		return nil, err
	}
	return jqvalue.ToEntries(jqvalue.Alt(v, jqvalue.NewObject()))
}
