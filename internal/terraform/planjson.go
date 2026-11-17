package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

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
// tfplan-<stage>.json files the template keeps.
func loadPlanJSON(ctx context.Context, rt *app.Runtime, cmd, path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, cmdError(rt.Stderr, cmd, "plan file not found: %s", path)
	}
	if isJSONFile(path) {
		b, err := os.ReadFile(path) //nolint:gosec // G304: the plan file the caller named
		if err != nil {
			return nil, cmdError(rt.Stderr, cmd, "%v", err)
		}
		return b, nil
	}
	// A plan file needs no workspace selection, so this runs bare terraform
	// rather than going through the workspace sequence. terraform's own
	// stderr passes through.
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

// isJSONFile reports whether the file starts, after white space, with '{'.
// A saved plan is a zip archive, which starts with "PK".
func isJSONFile(path string) bool {
	f, err := os.Open(path) //nolint:gosec // G304: the plan file the caller named
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	trimmed := bytes.TrimLeft(buf[:n], " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// planDoc is the part of `terraform show -json` both commands read.
type planDoc struct {
	// resource_drift is kept raw: only drift-check reads it (driftEntries),
	// and passes its entries through to the report unchanged.
	ResourceDrift   json.RawMessage  `json:"resource_drift"`
	ResourceChanges []resourceChange `json:"resource_changes"`
}

type resourceChange struct {
	Address string        `json:"address"`
	Change  *changeFields `json:"change"`
}

type changeFields struct {
	// Actions is nil when the plan has no actions (null or absent), and
	// empty for [].
	Actions []string `json:"actions"`
	// Importing is set (Terraform 1.5+) when the change imports the
	// resource; its action is then no-op or an update.
	Importing json.RawMessage `json:"importing"`
}

func (rc resourceChange) actions() []string {
	if rc.Change == nil {
		return nil
	}
	return rc.Change.Actions
}

func (rc resourceChange) importing() bool {
	if rc.Change == nil {
		return false
	}
	imp := bytes.TrimSpace(rc.Change.Importing)
	return len(imp) > 0 && !bytes.Equal(imp, []byte("null"))
}

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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return cmdError(rt.Stderr, cmd, "encode report: %v", err)
	}
	if path == "" {
		_, err := rt.Stdout.Write(buf.Bytes())
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil { //nolint:gosec // G306: a report other steps read
		return cmdError(rt.Stderr, cmd, "%v", err)
	}
	return nil
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
