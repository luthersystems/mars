package terraform

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

func TestSummarizePlan(t *testing.T) {
	plan, err := parsePlan([]byte(`{"resource_changes": [
  {"address": "a.create", "change": {"actions": ["create"]}},
  {"address": "a.update", "change": {"actions": ["update"]}},
  {"address": "a.delete", "change": {"actions": ["delete"]}},
  {"address": "a.cbd", "change": {"actions": ["create", "delete"]}},
  {"address": "a.dbc", "change": {"actions": ["delete", "create"]}},
  {"address": "a.noop", "change": {"actions": ["no-op"]}},
  {"address": "data.a.read", "change": {"actions": ["read"]}},
  {"address": "a.forget", "change": {"actions": ["forget"]}},
  {"address": "a.adopted", "change": {"actions": ["no-op"], "importing": {"id": "x"}}},
  {"address": "a.adopted_upd", "change": {"actions": ["update"], "importing": {"id": "y"}}},
  {"address": "a.legacy_import", "change": {"actions": ["import"]}},
  {"address": "a.null_importing", "change": {"actions": ["no-op"], "importing": null}},
  {"address": "a.nochange"}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := summarizePlan(plan)
	want := &PlanSummary{
		Add: 3, Change: 2, Destroy: 3, Replace: 2, Import: 3, HasChanges: true,
		Addresses: PlanAddresses{
			Create:  []string{"a.create"},
			Update:  []string{"a.update", "a.adopted_upd"},
			Delete:  []string{"a.delete"},
			Replace: []string{"a.cbd", "a.dbc"},
			Import:  []string{"a.adopted", "a.adopted_upd", "a.legacy_import"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

const emptySummary = `{
  "add": 0,
  "change": 0,
  "destroy": 0,
  "replace": 0,
  "import": 0,
  "has_changes": false,
  "addresses": {
    "create": [],
    "update": [],
    "delete": [],
    "replace": [],
    "import": []
  }
}
`

func TestPlanSummaryCmd(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		show       string
		showErr    error
		wantCode   int
		wantStdout string
		wantStderr string
		wantCmds   int
	}{
		{name: "binary plan, no changes", file: "PK\x03\x04", show: `{"format_version": "1.2"}`, wantStdout: emptySummary, wantCmds: 1},
		{name: "JSON plan needs no terraform", file: ` {"resource_changes": []}`, wantStdout: emptySummary},
		{name: "terraform show fails", file: "PK", showErr: &runner.ExitError{Code: 1}, wantCode: 1, wantStderr: "mars plan-summary: terraform show -json p failed (exit 1)", wantCmds: 1},
		{name: "bad JSON plan", file: `{"resource_changes": {}}`, wantCode: 1, wantStderr: "does not have terraform's shape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("p", []byte(c.file), 0o644); err != nil {
				t.Fatal(err)
			}
			fake := &runner.Fake{CaptureOut: [][]byte{[]byte(c.show)}, CaptureError: []error{c.showErr}}
			var stdout, stderr strings.Builder
			rt := &app.Runtime{Runner: fake, Stdout: &stdout, Stderr: &stderr}
			err := (&PlanSummaryCmd{PlanFile: "p"}).Run(context.Background(), rt)
			if code := runner.ExitCode(err); code != c.wantCode {
				t.Fatalf("exit %d, want %d: %v\nstderr: %s", code, c.wantCode, err, stderr.String())
			}
			if stdout.String() != c.wantStdout {
				t.Errorf("stdout:\n%s\nwant\n%s", stdout.String(), c.wantStdout)
			}
			if !strings.Contains(stderr.String(), c.wantStderr) {
				t.Errorf("stderr %q lacks %q", stderr.String(), c.wantStderr)
			}
			if n := len(fake.Commands()); n != c.wantCmds {
				t.Errorf("%d commands, want %d: %v", n, c.wantCmds, fake.Commands())
			}
		})
	}
}

func TestPlanSummaryOut(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("p.json", []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	rt := &app.Runtime{Runner: &runner.Fake{}, Stdout: &stdout, Stderr: &strings.Builder{}}
	if err := (&PlanSummaryCmd{PlanFile: "p.json", Out: "s.json"}).Run(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("s.json")
	if err != nil || string(b) != emptySummary || stdout.String() != "" {
		t.Errorf("out %q (%v), stdout %q", b, err, stdout.String())
	}
}
