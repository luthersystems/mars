package terraform

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

const driftPlanJSON = `{
  "resource_drift": [{"address": "aws_s3_bucket.example", "type": "aws_s3_bucket",
    "change": {"before": {"tags": {"a": "1"}, "acl": null, "n": 1.50}, "after": {"tags": {"a": "2"}, "acl": [], "n": 1.50}}}],
  "resource_changes": [{"address": "aws_s3_bucket.example", "change": {"actions": ["update"]}}]
}`

const wantDriftReport = `{
  "drift_detected": true,
  "drift_count": 1,
  "actionable": true,
  "actionable_count": 1,
  "apply_skipped": %s,
  "resources": [
    {
      "action": [
        "update"
      ],
      "address": "aws_s3_bucket.example",
      "change": {
        "after": {
          "acl": [],
          "n": 1.50,
          "tags": {
            "a": "2"
          }
        },
        "before": {
          "acl": null,
          "n": 1.50,
          "tags": {
            "a": "1"
          }
        }
      },
      "type": "aws_s3_bucket"
    }
  ]
}
`

const noDriftReport = `{
  "drift_detected": false,
  "drift_count": 0,
  "actionable": false,
  "actionable_count": 0,
  "apply_skipped": false,
  "resources": []
}
`

func TestDriftCheck(t *testing.T) {
	// Drift only in a computed attribute: the resource change is a no-op.
	computedOnly := strings.Replace(driftPlanJSON, `["update"]`, `["no-op"]`, 1)
	cases := []struct {
		name       string
		cmd        DriftCheckCmd
		show       string
		showErr    error
		noPlan     bool
		wantCode   int
		wantStdout string
		wantStderr []string
	}{
		{
			name:       "no drift",
			show:       `{"resource_drift": []}`,
			wantStdout: noDriftReport,
			wantStderr: []string{"No resource drift detected."},
		},
		{
			name:       "null-versus-empty only is not drift",
			show:       `{"resource_drift": [{"address": "a.b", "change": {"before": {"x": null, "y": 0}, "after": {"x": [], "y": false}}}]}`,
			wantStdout: noDriftReport,
		},
		{
			name:       "actionable drift exits 2",
			show:       driftPlanJSON,
			wantCode:   2,
			wantStdout: strings.Replace(wantDriftReport, "%s", "true", 1),
			wantStderr: []string{
				"Drift detected: 1 resource(s) have drifted.",
				"  aws_s3_bucket.example",
				`    tags: {"a":"1"} -> {"a":"2"}`,
				"actionable",
			},
		},
		{
			name:       "ignore-drift wins over strict",
			cmd:        DriftCheckCmd{IgnoreDrift: true, Strict: true},
			show:       driftPlanJSON,
			wantStdout: strings.Replace(wantDriftReport, "%s", "false", 1),
		},
		{
			name:       "drift that is not actionable exits 0",
			show:       computedOnly,
			wantStderr: []string{"not actionable"},
		},
		{
			name:     "strict exits 2 on drift that is not actionable",
			cmd:      DriftCheckCmd{Strict: true},
			show:     computedOnly,
			wantCode: 2,
		},
		{
			name:       "missing plan file",
			noPlan:     true,
			wantCode:   1,
			wantStderr: []string{"mars drift-check: plan file not found: test.tfplan"},
		},
		{
			name:       "terraform show fails",
			showErr:    &runner.ExitError{Code: 3},
			wantCode:   1,
			wantStderr: []string{"terraform show -json test.tfplan failed (exit 3)"},
		},
		{
			name:       "empty show output",
			show:       "\n",
			wantCode:   1,
			wantStderr: []string{"the plan JSON is empty"},
		},
		{
			name:       "plan that is not JSON",
			show:       `{"resource_drift": [`,
			wantCode:   1,
			wantStderr: []string{"does not have terraform's shape"},
		},
		{
			name:       "plan with the wrong shape",
			show:       `{"resource_drift": "x"}`,
			wantCode:   1,
			wantStderr: []string{"does not have terraform's shape"},
		},
		{
			name:       "before that is not an object",
			show:       `{"resource_drift": [{"address": "a.b", "change": {"before": "x", "after": {}}}]}`,
			wantCode:   1,
			wantStderr: []string{"resource_drift[0]: a.b: change.before is not an object"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if !c.noPlan {
				if err := os.WriteFile("test.tfplan", []byte("PK\x03\x04plan"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := c.cmd
			cmd.PlanFile = "test.tfplan"
			fake := &runner.Fake{CaptureOut: [][]byte{[]byte(c.show)}, CaptureError: []error{c.showErr}}
			var stdout, stderr strings.Builder
			rt := &app.Runtime{Target: "default", Runner: fake, Stdout: &stdout, Stderr: &stderr}

			err := cmd.Run(context.Background(), rt)
			if code := runner.ExitCode(err); code != c.wantCode {
				t.Fatalf("exit code %d, want %d (err %v)\nstdout:\n%s\nstderr:\n%s", code, c.wantCode, err, stdout.String(), stderr.String())
			}
			if err != nil && !runner.IsSilent(err) {
				t.Errorf("error %v is not silent; mars would print it", err)
			}
			if c.wantStdout != "" && stdout.String() != c.wantStdout {
				t.Errorf("stdout:\n%s\nwant\n%s", stdout.String(), c.wantStdout)
			}
			if c.wantCode == 1 && stdout.String() != "" {
				t.Errorf("stdout on error: %s", stdout.String())
			}
			for _, want := range c.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
				}
			}
			if !c.noPlan {
				want := [][]string{{"terraform", "show", "-json", "test.tfplan"}}
				if got := fake.Commands(); !reflect.DeepEqual(got, want) {
					t.Errorf("commands %v, want %v", got, want)
				}
			}
		})
	}
}

func TestDriftCheckJSONPlanAndOut(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("plan.json", []byte("\n  "+driftPlanJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "drift.json")
	fake := &runner.Fake{}
	var stdout, stderr strings.Builder
	rt := &app.Runtime{Runner: fake, Stdout: &stdout, Stderr: &stderr}
	err := (&DriftCheckCmd{PlanFile: "plan.json", Out: out}).Run(context.Background(), rt)
	if code := runner.ExitCode(err); code != 2 {
		t.Fatalf("exit %d, want 2: %v", code, err)
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("a JSON plan ran %v; want no terraform", fake.Commands())
	}
	if stdout.String() != "" {
		t.Errorf("stdout with --out: %q", stdout.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(wantDriftReport, "%s", "true", 1); string(b) != want {
		t.Errorf("--out file:\n%s\nwant\n%s", b, want)
	}
}

func TestDriftCheckActionEnrichment(t *testing.T) {
	plan := `{
  "resource_drift": [
    {"address": "a.none"},
    {"address": "a.read", "change": {"before": {"x": 1}, "after": {"x": 2}}},
    {"address": "a.dup", "change": {"before": {"x": 1}, "after": {"x": 2}}},
    {"address": "a.noactions", "change": {"before": {"x": 1}, "after": {"x": 2}}}
  ],
  "resource_changes": [
    {"address": "a.read", "change": {"actions": ["read"]}},
    {"address": "a.dup", "change": {"actions": ["delete", "create"]}},
    {"address": "a.dup", "change": {"actions": ["update"]}},
    {"address": "a.noactions", "change": {}}
  ]
}`
	r, err := buildDriftReport([]byte(plan), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, res := range r.Resources {
		got[res["address"].(string)] = res["action"]
	}
	want := map[string]any{
		"a.none":      nil,
		"a.read":      []string{"read"},
		"a.dup":       []string{"update"},
		"a.noactions": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actions %v, want %v", got, want)
	}
	// a.dup and a.noactions (no actions fails safe) are actionable.
	if r.DriftCount != 4 || r.ActionableCount != 2 || !r.ApplySkipped {
		t.Errorf("report %+v", r)
	}
}

func TestNormalizeEmpty(t *testing.T) {
	v, _ := decodeValue([]byte(`{"a": [0, "", {}], "b": {"c": false}, "d": "x", "e": -0.0}`))
	got := normalizeEmpty(v)
	want := map[string]any{"a": []any{nil, nil, nil}, "b": map[string]any{"c": nil}, "d": "x", "e": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestJSONEqualNumbers(t *testing.T) {
	a, _ := decodeValue([]byte(`[1e2, 1.0, 100000000000000000001]`))
	b, _ := decodeValue([]byte(`[100, 1, 100000000000000000000]`))
	if !jsonEqual(a, b) {
		t.Errorf("%v and %v should be equal", a, b)
	}
	c, _ := decodeValue([]byte(`[100, 2, 100000000000000000000]`))
	if jsonEqual(a, c) {
		t.Errorf("%v and %v should differ", a, c)
	}
}
