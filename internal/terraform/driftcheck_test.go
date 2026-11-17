package terraform

import (
	"context"
	"fmt"
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
    "change": {"before": {"tags": {"a": "1"}, "acl": null}, "after": {"tags": {"a": "2"}, "acl": []}}}],
  "resource_changes": [{"address": "aws_s3_bucket.example", "change": {"actions": ["update"]}}]
}`

const wantDriftReport = `{
  "drift_detected": true,
  "drift_count": 1,
  "actionable": true,
  "apply_skipped": %s,
  "template_version": %s,
  "presets_version": null,
  "resources": [
    {
      "address": "aws_s3_bucket.example",
      "type": "aws_s3_bucket",
      "change": {
        "before": {
          "tags": {
            "a": "1"
          },
          "acl": null
        },
        "after": {
          "tags": {
            "a": "2"
          },
          "acl": []
        }
      },
      "action": [
        "update"
      ]
    }
  ]
}
`

func TestDriftCheck(t *testing.T) {
	cases := []struct {
		name       string
		cmd        DriftCheckCmd
		show       string
		showErr    error
		noPlan     bool
		wantCode   int
		wantStdout []string
		wantStderr string
		wantFiles  map[string]string
	}{
		{
			name:       "no drift writes nothing",
			show:       `{"resource_drift": []}`,
			wantStdout: []string{"template_version=unknown", "presets_version=unknown", "No resource drift detected."},
			wantFiles:  map[string]string{},
		},
		{
			name:       "empty show output is no drift",
			show:       "\n",
			wantStdout: []string{"No resource drift detected."},
			wantFiles:  map[string]string{},
		},
		{
			name: "actionable drift skips apply and exits 0",
			cmd:  DriftCheckCmd{TemplateVersion: "v1"},
			show: driftPlanJSON,
			wantStdout: []string{
				"template_version=v1",
				"Drift detected: 1 resource(s) have drifted.",
				"  aws_s3_bucket.example",
				`    tags: {"a":"1"} -> {"a":"2"}`,
				"INFO: actionable drift detected; apply_skipped=true.",
			},
			wantFiles: map[string]string{"drift.json": fmt.Sprintf(wantDriftReport, "true", `"v1"`)},
		},
		{
			name:       "ignore-drift wins over strict",
			cmd:        DriftCheckCmd{IgnoreDrift: true, Strict: true},
			show:       driftPlanJSON,
			wantStdout: []string{"WARNING: Drift ignored (--ignore-drift flag set)"},
			wantFiles:  map[string]string{"drift.json": fmt.Sprintf(wantDriftReport, "false", "null")},
		},
		{
			name:       "strict exits 2",
			cmd:        DriftCheckCmd{Strict: true},
			show:       driftPlanJSON,
			wantCode:   2,
			wantFiles:  map[string]string{"drift.json": fmt.Sprintf(wantDriftReport, "true", "null")},
			wantStdout: []string{"Drift report written to "},
		},
		{
			name: "stage writes both files",
			cmd:  DriftCheckCmd{Stage: "cloud-provision"},
			show: driftPlanJSON,
			wantFiles: map[string]string{
				"drift.json":                 fmt.Sprintf(wantDriftReport, "true", "null"),
				"drift-cloud-provision.json": fmt.Sprintf(wantDriftReport, "true", "null"),
			},
			wantStdout: []string{"drift-cloud-provision.json (and drift.json)"},
		},
		{
			name:       "missing plan file",
			noPlan:     true,
			wantCode:   1,
			wantStderr: "ERROR: plan file not found: test.tfplan\n",
			wantFiles:  map[string]string{},
		},
		{
			name:      "terraform show exit code passes through",
			showErr:   &runner.ExitError{Code: 3},
			wantCode:  3,
			wantFiles: map[string]string{},
		},
		{
			name:       "plan that is not JSON",
			show:       `{"resource_drift": [`,
			wantCode:   5,
			wantStderr: "jq: parse error: unexpected end of JSON input\n",
			wantFiles:  map[string]string{},
		},
		{
			name:       "plan with the wrong shape",
			show:       `{"resource_drift": "x"}`,
			wantCode:   5,
			wantStderr: "jq: error (at <stdin>:1): Cannot iterate over string (\"x\")\n",
			wantFiles:  map[string]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if !c.noPlan {
				if err := os.WriteFile("test.tfplan", []byte("plan"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			outputs := filepath.Join(dir, "out")
			cmd := c.cmd
			cmd.PlanFile = "test.tfplan"
			cmd.OutputsDir = outputs
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
			for _, want := range c.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
				}
			}
			if c.wantStderr != "" && stderr.String() != c.wantStderr {
				t.Errorf("stderr %q, want %q", stderr.String(), c.wantStderr)
			}
			if !c.noPlan {
				want := [][]string{{"terraform", "show", "-json", "test.tfplan"}}
				if got := fake.Commands(); !reflect.DeepEqual(got, want) {
					t.Errorf("commands %v, want %v", got, want)
				}
			}
			got := map[string]string{}
			entries, _ := os.ReadDir(outputs)
			for _, e := range entries {
				b, _ := os.ReadFile(filepath.Join(outputs, e.Name()))
				got[e.Name()] = string(b)
			}
			for name, body := range c.wantFiles {
				if got[name] != body {
					t.Errorf("%s:\n%s\nwant\n%s", name, got[name], body)
				}
			}
			if len(got) != len(c.wantFiles) {
				t.Errorf("files %v, want %d", reflect.ValueOf(got).MapKeys(), len(c.wantFiles))
			}
		})
	}
}

func TestDriftCheckDefaultOutputsDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("p", []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARS_PROJECT_ROOT", filepath.Join(dir, "root"))
	fake := &runner.Fake{CaptureOut: [][]byte{[]byte(driftPlanJSON)}}
	var stdout strings.Builder
	rt := &app.Runtime{Runner: fake, Stdout: &stdout, Stderr: &strings.Builder{}}
	if err := (&DriftCheckCmd{PlanFile: "p"}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "root", "outputs", "drift.json")); err != nil {
		t.Errorf("drift.json not under $MARS_PROJECT_ROOT/outputs: %v", err)
	}

	t.Setenv("MARS_PROJECT_ROOT", "")
	c := &DriftCheckCmd{PlanFile: "p"}
	if got := c.outputsDir(); got != "/marsproject/outputs" {
		t.Errorf("default outputs dir %q, want /marsproject/outputs", got)
	}
}
