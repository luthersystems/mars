package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// Exit codes shared by drift-check and plan-summary.
const (
	exitOK    = 0
	exitError = 1
	// exitDrift is drift-check's "drift found" code (see DriftCheckCmd).
	exitDrift = 2
)

// cmdError prints "mars <cmd>: <msg>" and ends mars with exit code 1.
func cmdError(stderr io.Writer, cmd, format string, args ...any) error {
	fmt.Fprintf(stderr, "mars %s: %s\n", cmd, fmt.Sprintf(format, args...))
	return runner.ExitSilently(exitError)
}

// loadPlanJSON returns the JSON form of a plan. path is either a saved plan
// file (binary, read with `terraform show -json`) or a plan already rendered
// as JSON (a file whose first non-space byte is '{'), such as the
// tfplan-<stage>.json files the template keeps. The file is read once.
func loadPlanJSON(ctx context.Context, rt *app.Runtime, cmd, path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, cmdError(rt.Stderr, cmd, "plan file not found: %s", path)
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the plan file the caller named
	if err != nil {
		return nil, cmdError(rt.Stderr, cmd, "%v", err)
	}
	// A saved plan is a zip archive, which starts with "PK".
	if trimmed := bytes.TrimLeft(b, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '{' {
		return b, nil
	}
	// Install the project's pinned terraform first, as migration_fromplan
	// does: terraform show reads only plans of its own version. A plan file
	// needs no workspace selection. terraform's own stderr passes through.
	if err := newService(rt).tfenvInit(ctx); err != nil {
		return nil, err
	}
	out, err := rt.Runner.Capture(ctx, runner.Command{
		Argv:   []string{"terraform", "show", "-json", path},
		Stderr: rt.Stderr,
		Quiet:  true,
	})
	if err != nil {
		var exitErr *runner.ExitError
		if errors.As(err, &exitErr) {
			return nil, cmdError(rt.Stderr, cmd, "terraform show -json %s failed (exit %d)", path, exitErr.Code)
		}
		return nil, cmdError(rt.Stderr, cmd, "terraform show -json %s: %v", path, err)
	}
	return out, nil
}

// planDoc is the part of `terraform show -json` both commands read. It is
// not tfjson.Plan, whose decoder insists on a format_version.
type planDoc struct {
	// resource_drift is kept raw: only drift-check reads it (driftEntries),
	// and passes its entries through to the report unchanged.
	ResourceDrift   json.RawMessage          `json:"resource_drift"`
	ResourceChanges []*tfjson.ResourceChange `json:"resource_changes"`
}

// changeActions is rc.change.actions: nil when the change or its actions
// are missing or null, empty for [].
func changeActions(rc *tfjson.ResourceChange) tfjson.Actions {
	if rc == nil || rc.Change == nil {
		return nil
	}
	return rc.Change.Actions
}

// importing reports whether the change imports the resource:
// change.importing (Terraform 1.5+), or an "import" action, which the
// template's fixtures use.
func importing(rc *tfjson.ResourceChange) bool {
	if rc != nil && rc.Change != nil && rc.Change.Importing != nil {
		return true
	}
	return slices.Contains(changeActions(rc), tfjson.Action("import"))
}

// noOpOrRead is a change Terraform will not act on.
func noOpOrRead(a tfjson.Actions) bool { return a.NoOp() || a.Read() }

// parsePlan decodes the plan. An empty document is an error: terraform show
// -json always prints an object, so empty output means something failed.
func parsePlan(raw []byte) (*planDoc, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("the plan JSON is empty")
	}
	var p planDoc
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("the plan JSON does not have terraform's shape: %w", err)
	}
	return &p, nil
}

// driftEntries is resource_drift as a list of raw entries (none when
// absent or null).
func (p *planDoc) driftEntries() ([]json.RawMessage, error) {
	raw := bytes.TrimSpace(p.ResourceDrift)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.New("the plan JSON does not have terraform's shape: resource_drift is not an array")
	}
	return entries, nil
}

// decodeValue decodes JSON keeping number literals (json.Number), so a value
// passed through is written back as it came.
func decodeValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// writeReport writes v as indented JSON to path, or to stdout when path is
// empty. Struct field order is the report's order; map keys are sorted.
func writeReport(rt *app.Runtime, cmd, path string, v any) error {
	b, err := encodeJSON(v, true)
	if err != nil {
		return cmdError(rt.Stderr, cmd, "encode report: %v", err)
	}
	if path == "" {
		_, err := rt.Stdout.Write(b)
		return err
	}
	if err := writeReportFile(path, b); err != nil {
		return cmdError(rt.Stderr, cmd, "%v", err)
	}
	return nil
}

// encodeJSON encodes v without HTML escaping: indented with a final
// newline (a report file), or compact without one.
func encodeJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if indent {
		return buf.Bytes(), nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// writeReportFile writes b to path, making its directory. An empty path
// writes nothing.
func writeReportFile(path string, b []byte) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: as the script's mkdir -p of the outputs directory
		return err
	}
	return os.WriteFile(path, b, 0o644) //nolint:gosec // G306: a report other steps read
}

// writeReportJSON writes v to path as writeReport does; an empty path
// writes nothing.
func writeReportJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := encodeJSON(v, true)
	if err != nil {
		return err
	}
	return writeReportFile(path, b)
}

// jsonEqual compares decoded JSON values; numbers compare by value, as jq
// compares them (1.0 equals 1).
func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case json.Number:
		bv, ok := b.(json.Number)
		if !ok {
			return false
		}
		af, aerr := strconv.ParseFloat(string(av), 64)
		bf, berr := strconv.ParseFloat(string(bv), 64)
		if aerr != nil || berr != nil {
			return av == bv
		}
		return af == bf
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !jsonEqual(x, y) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// isZeroNumber reports whether n is 0 (0, -0, 0.0, 0e5).
func isZeroNumber(n json.Number) bool {
	f, err := strconv.ParseFloat(string(n), 64)
	return err == nil && f == 0
}
