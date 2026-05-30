package reverseimportjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luthersystems/insideout-terraform-presets/pkg/composer/imported"
	"github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport"
	reversejob "github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport/job"
)

func TestMainRequiresRequest(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Main(context.Background(), nil, &stdout, &stderr, nil)

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr.String(), "--request is required") {
		t.Fatalf("stderr = %q, want required request error", stderr.String())
	}
}

func TestMainDecodesRequestAndPassesOptions(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	writeRequest(t, requestPath)

	var gotReq reversejob.Request
	var gotOpts reverseimport.Options
	before := time.Now().UTC()
	run := func(_ context.Context, req reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		gotReq = req
		gotOpts = opts
		return reversejob.Result{
			Version: reversejob.Version,
			Status:  reversejob.StatusSucceeded,
			PlanSummary: reversejob.PlanSummary{
				ImportCount:  1,
				AddCount:     0,
				ChangeCount:  0,
				DestroyCount: 0,
			},
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", filepath.Join(dir, "out"),
		"--work-dir", filepath.Join(dir, "work"),
		"--cloud", "aws",
		"--region", "us-west-2",
		"--gcp-project-id", "gcp-project",
		"--aws-endpoint-url", "http://localhost:4566",
		"--import-project-id", "io-project",
		"--import-session-id", "session-1",
		"--terraform-binary", "/opt/tfenv/bin/terraform",
	}, &stdout, &stderr, run)
	after := time.Now().UTC()

	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
	}
	if len(gotReq.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(gotReq.Resources))
	}
	if gotReq.Resources[0].Identity.Address != "aws_s3_bucket.example" {
		t.Fatalf("address = %q", gotReq.Resources[0].Identity.Address)
	}
	if gotOpts.OutputDir != filepath.Join(dir, "out") {
		t.Fatalf("OutputDir = %q", gotOpts.OutputDir)
	}
	if gotOpts.Workdir != filepath.Join(dir, "work") {
		t.Fatalf("Workdir = %q", gotOpts.Workdir)
	}
	if gotOpts.Cloud != "aws" || gotOpts.Region != "us-west-2" {
		t.Fatalf("cloud/region = %q/%q", gotOpts.Cloud, gotOpts.Region)
	}
	if gotOpts.AWSEndpointURL != "http://localhost:4566" {
		t.Fatalf("AWSEndpointURL = %q", gotOpts.AWSEndpointURL)
	}
	if gotOpts.GCPProjectID != "gcp-project" {
		t.Fatalf("GCPProjectID = %q", gotOpts.GCPProjectID)
	}
	if gotOpts.ImportProjectID != "io-project" || gotOpts.ImportSessionID != "session-1" {
		t.Fatalf("import provenance = %q/%q", gotOpts.ImportProjectID, gotOpts.ImportSessionID)
	}
	if gotOpts.ImportedAt.IsZero() || gotOpts.ImportedAt.Before(before) || gotOpts.ImportedAt.After(after) {
		t.Fatalf("ImportedAt = %s, want between %s and %s", gotOpts.ImportedAt, before, after)
	}
	if gotOpts.TerraformBinary != "/opt/tfenv/bin/terraform" {
		t.Fatalf("TerraformBinary = %q", gotOpts.TerraformBinary)
	}
	// The engine progress sink must be the job's own stdout writer, so the
	// live phase progress and silent-phase heartbeat (presets #702) reach
	// Oracle's follow=1 stream. A nil sink would discard them and the job
	// log would look frozen again.
	if gotOpts.Stdout != io.Writer(&stdout) {
		t.Fatalf("Options.Stdout = %v, want the job stdout writer", gotOpts.Stdout)
	}
	if !strings.Contains(stdout.String(), "reverse import succeeded: 1 imported, 0 add, 0 change, 0 destroy") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestMainUsesDefaultOutputDir(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	writeRequest(t, requestPath)

	var gotOutputDir string
	run := func(_ context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		gotOutputDir = opts.OutputDir
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusSucceeded}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--request", requestPath}, &stdout, &stderr, run)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
	}
	if gotOutputDir != DefaultOutputDir {
		t.Fatalf("OutputDir = %q, want %q", gotOutputDir, DefaultOutputDir)
	}
}

func TestMainRejectsUnreadableOrMalformedRequestBeforeCallingSDK(t *testing.T) {
	dir := t.TempDir()
	invalidPath := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		requestPath string
		wantStderr  string
	}{
		{
			name:        "missing",
			requestPath: filepath.Join(dir, "missing.json"),
			wantStderr:  "open --request:",
		},
		{
			name:        "malformed",
			requestPath: invalidPath,
			wantStderr:  "decode --request:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			run := func(_ context.Context, _ reversejob.Request, _ reverseimport.Options) (reversejob.Result, error) {
				called = true
				return reversejob.Result{}, nil
			}
			var stdout, stderr bytes.Buffer

			code := Main(context.Background(), []string{"--request", tt.requestPath}, &stdout, &stderr, run)

			if code == 0 {
				t.Fatalf("exit code = %d, want non-zero", code)
			}
			if called {
				t.Fatal("runner was called for an invalid request")
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestMainLeavesSDKArtifactsInOutputDir(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	writeRequest(t, requestPath)

	expectedFiles := map[string]string{
		"reverse-result.json": "result",
		"imported.json":       "imported-json",
		"imported.tf":         "imported-tf",
		"validate.json":       "validate-json",
		"tfplan.json":         "tfplan-json",
		"plan-summary.json":   "plan-summary-json",
	}
	run := func(_ context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
			return reversejob.Result{}, err
		}
		for name, body := range expectedFiles {
			if err := os.WriteFile(filepath.Join(opts.OutputDir, name), []byte(body), 0o644); err != nil {
				return reversejob.Result{}, err
			}
		}
		return reversejob.Result{
			Version: reversejob.Version,
			Status:  reversejob.StatusSucceeded,
			PlanSummary: reversejob.PlanSummary{
				ImportCount: 1,
			},
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--request", requestPath, "--out-dir", outputDir}, &stdout, &stderr, run)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
	}
	for name, wantBody := range expectedFiles {
		body, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(body) != wantBody {
			t.Fatalf("%s = %q, want %q", name, body, wantBody)
		}
	}
}

func TestMainWritesFailureResultWhenSDKFailsBeforeResultArtifact(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	writeRequest(t, requestPath)
	runErr := errors.New("terraform validate final: invalid configuration")

	run := func(_ context.Context, _ reversejob.Request, _ reverseimport.Options) (reversejob.Result, error) {
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusFailed}, runErr
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--request", requestPath, "--out-dir", outputDir}, &stdout, &stderr, run)

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	body, err := os.ReadFile(filepath.Join(outputDir, resultFile))
	if err != nil {
		t.Fatalf("read failure result: %v", err)
	}
	var result reversejob.Result
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != reversejob.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Message != runErr.Error() {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestMainWritesFailureResultWhenSDKReturnsFailedStatus(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	writeRequest(t, requestPath)

	run := func(_ context.Context, _ reversejob.Request, _ reverseimport.Options) (reversejob.Result, error) {
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusFailed}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--request", requestPath, "--out-dir", outputDir}, &stdout, &stderr, run)

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr.String(), `reverse import ended with status "failed"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(outputDir, resultFile))
	if err != nil {
		t.Fatalf("read failure result: %v", err)
	}
	var result reversejob.Result
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != reversejob.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "status") {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestMainPreservesExistingFailureResult(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	writeRequest(t, requestPath)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(outputDir, resultFile)
	const existing = `{"version":1,"status":"failed","diagnostics":[{"message":"from sdk"}]}`
	if err := os.WriteFile(resultPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(_ context.Context, _ reversejob.Request, _ reverseimport.Options) (reversejob.Result, error) {
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusFailed}, errors.New("later failure")
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--request", requestPath, "--out-dir", outputDir}, &stdout, &stderr, run)

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	body, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(body) != existing {
		t.Fatalf("result was overwritten:\n%s", body)
	}
}

func writeRequest(t *testing.T, path string) {
	t.Helper()
	req := reversejob.Request{
		Version: reversejob.Version,
		Resources: []reversejob.ResourceSpec{{
			Identity: imported.ResourceIdentity{
				Cloud:    "aws",
				Type:     "aws_s3_bucket",
				Address:  "aws_s3_bucket.example",
				Region:   "us-west-2",
				ImportID: "example-bucket",
			},
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
