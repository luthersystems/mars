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

// TestPlanStageWritesRedactedPlanJSON: plan-stage, with and without
// --refresh-only, writes the stage's plan JSON and the tfplan.json alias
// redacted.
func TestPlanStageWritesRedactedPlanJSON(t *testing.T) {
	for _, refreshOnly := range []bool{false, true} {
		root, f := stageProject(t, twoStageConfig)
		f.plans["a"] = planWithCredentialVar
		rt, _, stderr := stageRuntime(f)
		if err := (&PlanStageCmd{Stage: "a", RefreshOnly: refreshOnly}).Run(context.Background(), rt); err != nil {
			t.Fatalf("plan-stage (refresh-only %v): %v\n%s", refreshOnly, err, stderr)
		}
		for _, name := range []string{"tfplan-a.json", "tfplan.json"} {
			b, err := os.ReadFile(filepath.Join(root, "outputs", name))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "c3RhZ2UtcGxhbi1sZWFrLXZhbHVl") {
				t.Errorf("refresh-only %v: %s still holds the credential tfvar value", refreshOnly, name)
			}
		}
	}
}

// TestPlanAllContinueRedactsOldAlias: with --continue, the alias may come
// from a stage plan JSON an older mars wrote in clear; the alias is
// redacted all the same.
func TestPlanAllContinueRedactsOldAlias(t *testing.T) {
	root, f := stageProject(t, twoStageConfig)
	f.plans["a"] = planWithCredentialVar
	rt, _, stderr := stageRuntime(f)
	if err := os.MkdirAll(filepath.Join(root, "outputs"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Stage b is plan_alias_preferred; its file is from an older run.
	if err := os.WriteFile(filepath.Join(root, "outputs", "tfplan-b.json"), []byte(planWithCredentialVar), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&PlanAllCmd{Stages: []string{"a"}, Continue: true}).Run(context.Background(), rt); err != nil {
		t.Fatalf("plan-all --continue: %v\n%s", err, stderr)
	}
	b, err := os.ReadFile(filepath.Join(root, "outputs", "tfplan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "c3RhZ2UtcGxhbi1sZWFrLXZhbHVl") {
		t.Errorf("tfplan.json copied an old unredacted plan:\n%s", b)
	}
	if !strings.Contains(string(b), "aws_s3_bucket.a") {
		t.Errorf("tfplan.json is not stage b's plan:\n%s", b)
	}
}
