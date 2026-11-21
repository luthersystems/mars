package terraform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planWithCredentialVar is a stage plan as `terraform show -json` prints
// it once the stage's credential tfvars are set: the value sits in
// .variables in clear text (luthersystems/reliable#2553).
const planWithCredentialVar = `{
  "format_version": "1.2",
  "variables": {"gcp_credentials_b64": {"value": "c3RhZ2UtcGxhbi1sZWFrLXZhbHVl"}},
  "resource_changes": [
    {"address": "aws_s3_bucket.a", "mode": "managed", "type": "aws_s3_bucket",
     "change": {"actions": ["delete"], "before": {"bucket": "a"}, "after": null}}
  ]
}`

// TestPlanAllWritesRedactedPlanJSON: the per-stage plan JSON and the
// tfplan.json alias the template uploads hold no tfvar value, and the
// summary, read from the exact plan, still counts the change.
func TestPlanAllWritesRedactedPlanJSON(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planWithCredentialVar
	f.plans["b"] = planWithCredentialVar
	rt, _, stderr := stageRuntime(f)

	if err := (&PlanAllCmd{}).Run(context.Background(), rt); err != nil {
		t.Fatalf("plan-all: %v\n%s", err, stderr)
	}
	for _, name := range []string{"tfplan-a.json", "tfplan-b.json", "tfplan.json"} {
		b, err := os.ReadFile(filepath.Join(root, "outputs", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "c3RhZ2UtcGxhbi1sZWFrLXZhbHVl") {
			t.Errorf("%s still holds the credential tfvar value:\n%s", name, b)
		}
		if !strings.Contains(string(b), `"gcp_credentials_b64"`) || !strings.Contains(string(b), "aws_s3_bucket.a") {
			t.Errorf("%s lost the variable name or the resource change:\n%s", name, b)
		}
	}
	summary, err := os.ReadFile(filepath.Join(root, "outputs", "plan-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), `"destroy": 2`) {
		t.Errorf("summary does not count the exact plan's deletes:\n%s", summary)
	}
}
