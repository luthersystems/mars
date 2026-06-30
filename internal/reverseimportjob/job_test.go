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

	"github.com/luthersystems/insideout-terraform-presets/cmd/insideout-import/reversedisco"
	"github.com/luthersystems/insideout-terraform-presets/pkg/composer/imported"
	"github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport"
	reversejob "github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport/job"
)

// fakeClosureDiscoverer is a stub cloud discoverer used by unit tests that
// drive Main without standing up real AWS/GCP clients. It implements both
// reverseimport.Discoverer (dep-chase) and reverseimport.ClosureDiscoverer
// (selection-closure expansion), so when Main wires it onto the engine
// Options the engine does NOT emit the selection_closure_unavailable
// diagnostic (luthersystems/mars#195).
type fakeClosureDiscoverer struct{}

func (fakeClosureDiscoverer) DiscoverByID(context.Context, string, string, string, string) (imported.ImportedResource, error) {
	return imported.ImportedResource{}, nil
}

func (fakeClosureDiscoverer) DiscoverClosure(context.Context, reverseimport.ClosureRequest) ([]imported.ImportedResource, error) {
	return nil, nil
}

// useFakeDiscoverer swaps the package-level production discoverer factory for
// a fake that never touches the cloud, restoring the original when the test
// ends. Tests that exercise Main's request/Options plumbing (rather than the
// discoverer wiring specifically) use this so the empty/arbitrary --cloud
// values they pass don't trip reversedisco.New's unknown-cloud guard.
func useFakeDiscoverer(t *testing.T) {
	t.Helper()
	orig := newDiscoverer
	newDiscoverer = func(context.Context, string, string, string, string, reversedisco.AWSAssumeRole) (reverseimport.Discoverer, func(), error) {
		return fakeClosureDiscoverer{}, func() {}, nil
	}
	t.Cleanup(func() { newDiscoverer = orig })
}

// useRecordingDiscoverer swaps in a fake factory that records the
// reversedisco.AWSAssumeRole it was constructed with. *got is only populated
// once the lazyDiscoverer is first used; the run closure used with it (see
// discovererProbeRunner) must trigger that first use so the factory fires.
func useRecordingDiscoverer(t *testing.T, got *reversedisco.AWSAssumeRole) {
	t.Helper()
	orig := newDiscoverer
	newDiscoverer = func(_ context.Context, _, _, _, _ string, awsAuth reversedisco.AWSAssumeRole) (reverseimport.Discoverer, func(), error) {
		*got = awsAuth
		return fakeClosureDiscoverer{}, func() {}, nil
	}
	t.Cleanup(func() { newDiscoverer = orig })
}

// discovererProbeRunner is a stand-in for reverseimport.Run that forces the
// lazyDiscoverer Main wired to build (triggering the recording factory) by
// calling one discoverer method, then returns success — without standing up
// real terraform/genconfig/driftfix. Using run=nil here would drive the real
// engine past the closure phase into terraform init/plan, making the unit
// suite depend on a local/CI terraform install (codex P2 on #198).
func discovererProbeRunner(t *testing.T) Runner {
	t.Helper()
	return func(ctx context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		if opts.Discoverer == nil {
			t.Fatal("Options.Discoverer = nil: Main did not wire the closure discoverer")
		}
		// First use of the lazy discoverer builds it via the factory, which is
		// exactly what records the auth. The fake factory's DiscoverByID no-ops.
		if _, err := opts.Discoverer.DiscoverByID(ctx, "aws_kms_key", "id", "us-west-2", ""); err != nil {
			t.Fatalf("DiscoverByID: %v", err)
		}
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusSucceeded}, nil
	}
}

// TestMainResolvesAndPassesAssumeRoleAuth is the regression guard for
// presets#770 in the deployed (Mars job pod) context. The pod runs as the
// platform Argo role; terraform reaches the customer account via generated
// assume_role { role_arn } blocks. The closure/dep-chase discoverer hits the
// AWS SDK directly, so Main MUST resolve the same customer assume-role identity
// (TF_VAR_bootstrap_role / TF_VAR_aws_external_id) and thread it to the
// discoverer factory — otherwise the SDK calls run as the wrong principal and
// the customer account returns AccessDenied (presets#739 defect 2).
//
// We set the TF_VAR_* env and drive Main with a probe runner that forces the
// lazy discoverer to build (the recording factory then captures the auth),
// without invoking real terraform. We assert the factory received exactly that
// RoleARN/ExternalID. If the wiring drops the auth (e.g. passes an empty
// struct), got stays zero and this fails — proving the wiring is load-bearing.
func TestMainResolvesAndPassesAssumeRoleAuth(t *testing.T) {
	const (
		wantRoleARN    = "arn:aws:iam::123456789012:role/insideout-bootstrap"
		wantExternalID = "io-external-id-xyz"
	)
	t.Setenv("TF_VAR_bootstrap_role", wantRoleARN)
	t.Setenv("TF_VAR_aws_external_id", wantExternalID)

	var got reversedisco.AWSAssumeRole
	useRecordingDiscoverer(t, &got)

	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	writeRequest(t, requestPath)

	var stdout, stderr bytes.Buffer
	Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", filepath.Join(dir, "out"),
		"--cloud", "aws",
		"--region", "us-west-2",
	}, &stdout, &stderr, discovererProbeRunner(t))

	if got.RoleARN != wantRoleARN {
		t.Fatalf("factory RoleARN = %q, want %q (auth not threaded to the discoverer — "+
			"closure SDK calls would run as the wrong principal, presets#770/#739)\nstderr:\n%s",
			got.RoleARN, wantRoleARN, stderr.String())
	}
	if got.ExternalID != wantExternalID {
		t.Fatalf("factory ExternalID = %q, want %q", got.ExternalID, wantExternalID)
	}
}

// TestMainPassesEmptyAssumeRoleAuthWhenUnset asserts the ambient-credentials
// path: with no TF_VAR_* env and no output-dir artifacts, Main resolves an empty
// reversedisco.AWSAssumeRole and passes it through, so the discoverer uses
// ambient creds unchanged (the correct local-CLI behavior).
func TestMainPassesEmptyAssumeRoleAuthWhenUnset(t *testing.T) {
	// Defensively clear in case the ambient test env carries these.
	t.Setenv("TF_VAR_bootstrap_role", "")
	t.Setenv("TF_VAR_aws_external_id", "")

	var got reversedisco.AWSAssumeRole
	useRecordingDiscoverer(t, &got)

	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	writeRequest(t, requestPath)

	var stdout, stderr bytes.Buffer
	Main(context.Background(), []string{
		"--request", requestPath,
		// out-dir has no tf/auto-vars or outputs/cloud-provision.json, so the
		// file-based resolution legs find nothing and auth stays empty.
		"--out-dir", filepath.Join(dir, "out"),
		"--cloud", "aws",
		"--region", "us-west-2",
	}, &stdout, &stderr, discovererProbeRunner(t))

	if got.RoleARN != "" || got.ExternalID != "" {
		t.Fatalf("factory auth = %+v, want empty (ambient credentials)\nstderr:\n%s", got, stderr.String())
	}
}

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
	if len(gotOpts.DiscoverRegions) != 1 || gotOpts.DiscoverRegions[0] != "us-west-2" {
		t.Fatalf("DiscoverRegions = %v, want [us-west-2]", gotOpts.DiscoverRegions)
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
	// The closure-capable cloud discoverer must be wired onto the engine
	// Options — otherwise the engine emits selection_closure_unavailable and
	// skips dependency-closure expansion + dep-chase (luthersystems/mars#195).
	// This case uses the real production newDiscoverer (cloud=aws, which only
	// loads SDK config, no network), so it exercises the production wiring.
	if gotOpts.Discoverer == nil {
		t.Fatal("Options.Discoverer = nil, want a closure-capable cloud discoverer")
	}
	if _, ok := gotOpts.Discoverer.(reverseimport.ClosureDiscoverer); !ok {
		t.Fatalf("Options.Discoverer %T does not implement reverseimport.ClosureDiscoverer; "+
			"the engine would emit selection_closure_unavailable", gotOpts.Discoverer)
	}
	if !strings.Contains(stdout.String(), "reverse import succeeded: 1 imported, 0 add, 0 change, 0 destroy") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// TestMainWiresClosureDiscovererForRegisteredParents is the regression guard
// for luthersystems/mars#195. The reverse-import engine only runs
// selection-closure expansion + dep-chase when Options.ClosureDiscoverer is
// set or Options.Discoverer is itself closure-capable; otherwise it emits the
// selection_closure_unavailable diagnostic and silently skips both
// (pkg/reverseimport/closure.go). Before the fix, Main set neither field.
//
// We select a registered parent type (aws_kms_key has registered child
// aws_kms_alias in the presets labels registry), then assert Main passes the
// engine a non-nil Discoverer that implements reverseimport.ClosureDiscoverer
// — exactly the condition the engine checks before deciding whether to expand
// the closure. With the real production newDiscoverer (cloud=aws), this proves
// the wiring is present end to end without standing up real AWS or terraform.
func TestMainWiresClosureDiscovererForRegisteredParents(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	req := reversejob.Request{
		Version: reversejob.Version,
		Resources: []reversejob.ResourceSpec{{
			Identity: imported.ResourceIdentity{
				Cloud:    "aws",
				Type:     "aws_kms_key",
				Address:  "aws_kms_key.example",
				Region:   "us-west-2",
				ImportID: "1234abcd-12ab-34cd-56ef-1234567890ab",
			},
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var gotOpts reverseimport.Options
	run := func(_ context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		gotOpts = opts
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusSucceeded}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", filepath.Join(dir, "out"),
		"--cloud", "aws",
		"--region", "us-west-2",
	}, &stdout, &stderr, run)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
	}
	if gotOpts.Discoverer == nil {
		t.Fatal("Options.Discoverer = nil: engine would emit selection_closure_unavailable " +
			"and skip closure expansion + dep-chase (#195)")
	}
	if _, ok := gotOpts.Discoverer.(reverseimport.ClosureDiscoverer); !ok {
		t.Fatalf("Options.Discoverer %T does not implement reverseimport.ClosureDiscoverer: "+
			"engine would emit selection_closure_unavailable (#195)", gotOpts.Discoverer)
	}
}

// TestMainValidatesSelectionBeforeBuildingCloudClient guards the lazy-discoverer
// ordering (codex P2 on #196). A failing discoverer factory (e.g. GCP ADC
// missing / Cloud Asset API disabled) must NOT mask the engine's structured
// selection validation: an InsideOutImported selection should still be rejected
// with the validation result, and the cloud client must never be constructed
// because validation rejects before the closure phase that needs it.
func TestMainValidatesSelectionBeforeBuildingCloudClient(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	req := reversejob.Request{
		Version: reversejob.Version,
		Resources: []reversejob.ResourceSpec{{
			Identity: imported.ResourceIdentity{
				Cloud:    "aws",
				Type:     "aws_s3_bucket",
				Address:  "aws_s3_bucket.example",
				Region:   "us-west-2",
				ImportID: "example-bucket",
				Tags: map[string]string{
					"InsideOutImported":      "true",
					"InsideOutImportProject": "4b982735-ff89-4295-a3fa-8a75a554ffc9",
				},
			},
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	// The factory errors like a missing-credential / disabled-API path would,
	// and records whether it was ever called.
	built := false
	orig := newDiscoverer
	newDiscoverer = func(context.Context, string, string, string, string, reversedisco.AWSAssumeRole) (reverseimport.Discoverer, func(), error) {
		built = true
		return nil, func() {}, errors.New("cloud asset client unavailable: ADC missing")
	}
	t.Cleanup(func() { newDiscoverer = orig })

	var stdout, stderr bytes.Buffer
	// run=nil → the real reverseimport.Run, which runs selection validation
	// before any closure/dep-chase discovery.
	code := Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", outputDir,
		"--import-project-id", "io-current",
	}, &stdout, &stderr, nil)

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if built {
		t.Fatal("cloud discoverer was constructed before selection validation; " +
			"a credential/API gap would mask the validation result (#196)")
	}
	if !strings.Contains(stderr.String(), "selected resource cannot be imported") {
		t.Fatalf("stderr = %q, want the structured unimportable-selection rejection "+
			"(not a discoverer-construction error)", stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(outputDir, resultFile))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result reversejob.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if len(result.ValidationIssues) != 1 || result.ValidationIssues[0].Code != imported.ReasonInsideOutImported {
		t.Fatalf("validation issues = %#v, want insideout_imported", result.ValidationIssues)
	}
}

// TestMainScansAllSelectedRegionsForClosure guards the multi-region closure
// case (codex P2 on #196): when a request selects parents across several AWS
// regions and no --region override is passed, closure discovery must scan
// every distinct region, not just the first. The engine reads
// Options.DiscoverRegions for closure scope and only falls back to a single
// Region when DiscoverRegions is empty.
func TestMainScansAllSelectedRegionsForClosure(t *testing.T) {
	useFakeDiscoverer(t)
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	req := reversejob.Request{
		Version: reversejob.Version,
		Resources: []reversejob.ResourceSpec{
			{Identity: imported.ResourceIdentity{Cloud: "aws", Type: "aws_kms_key", Address: "aws_kms_key.east", Region: "us-east-1", ImportID: "key-east"}},
			{Identity: imported.ResourceIdentity{Cloud: "aws", Type: "aws_kms_key", Address: "aws_kms_key.west", Region: "us-west-2", ImportID: "key-west"}},
			// Duplicate region should be de-duplicated.
			{Identity: imported.ResourceIdentity{Cloud: "aws", Type: "aws_kms_key", Address: "aws_kms_key.west2", Region: "us-west-2", ImportID: "key-west2"}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var gotOpts reverseimport.Options
	run := func(_ context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		gotOpts = opts
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusSucceeded}, nil
	}

	var stdout, stderr bytes.Buffer
	// No --region override: closure scope is derived from the request.
	code := Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", filepath.Join(dir, "out"),
	}, &stdout, &stderr, run)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
	}
	want := []string{"us-east-1", "us-west-2"}
	if len(gotOpts.DiscoverRegions) != len(want) {
		t.Fatalf("DiscoverRegions = %v, want %v", gotOpts.DiscoverRegions, want)
	}
	for i, region := range want {
		if gotOpts.DiscoverRegions[i] != region {
			t.Fatalf("DiscoverRegions = %v, want %v", gotOpts.DiscoverRegions, want)
		}
	}
}

// TestMainRegionOverrideWinsForClosureScope asserts that an explicit --region
// override pins closure discovery to that single region even when the request
// spans others, mirroring the CLI reverse path.
func TestMainRegionOverrideWinsForClosureScope(t *testing.T) {
	useFakeDiscoverer(t)
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	req := reversejob.Request{
		Version: reversejob.Version,
		Resources: []reversejob.ResourceSpec{
			{Identity: imported.ResourceIdentity{Cloud: "aws", Type: "aws_kms_key", Address: "aws_kms_key.east", Region: "us-east-1", ImportID: "key-east"}},
			{Identity: imported.ResourceIdentity{Cloud: "aws", Type: "aws_kms_key", Address: "aws_kms_key.west", Region: "us-west-2", ImportID: "key-west"}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var gotOpts reverseimport.Options
	run := func(_ context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		gotOpts = opts
		return reversejob.Result{Version: reversejob.Version, Status: reversejob.StatusSucceeded}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", filepath.Join(dir, "out"),
		"--region", "eu-west-1",
	}, &stdout, &stderr, run)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
	}
	if len(gotOpts.DiscoverRegions) != 1 || gotOpts.DiscoverRegions[0] != "eu-west-1" {
		t.Fatalf("DiscoverRegions = %v, want [eu-west-1]", gotOpts.DiscoverRegions)
	}
	if gotOpts.Region != "eu-west-1" {
		t.Fatalf("Region = %q, want eu-west-1", gotOpts.Region)
	}
}

func TestMainUsesDefaultOutputDir(t *testing.T) {
	useFakeDiscoverer(t)
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
	useFakeDiscoverer(t)
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
	useFakeDiscoverer(t)
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
	useFakeDiscoverer(t)
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

// TestMainTreatsPartialStatusAsSuccess pins the whole-account-import fix: the
// partial-tolerant engine (presets#732/#734) returns StatusPartial when it
// imported every plannable resource and isolated the genuinely-unimportable
// ones. mars must treat that as a USABLE success (exit 0) — not fail the whole
// job — so a whole-account import with a couple of unimportable orphans still
// lands the rest. Regression target: account 141812438321 (361 imported, 2
// orphan-skipped → partial) used to exit 1 → JOB_STATUS_FAILURE.
func TestMainTreatsPartialStatusAsSuccess(t *testing.T) {
	useFakeDiscoverer(t)
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	writeRequest(t, requestPath)

	const partialResult = `{"version":1,"status":"partial","resources":[{"status":"skipped"}]}`
	run := func(_ context.Context, _ reversejob.Request, opts reverseimport.Options) (reversejob.Result, error) {
		if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
			return reversejob.Result{}, err
		}
		// The engine writes result.json itself on the non-failure path; emulate
		// that so we can assert mars does NOT clobber it via ensureFailureResult.
		if err := os.WriteFile(filepath.Join(opts.OutputDir, resultFile), []byte(partialResult), 0o644); err != nil {
			return reversejob.Result{}, err
		}
		return reversejob.Result{
			Version:     reversejob.Version,
			Status:      reversejob.StatusPartial,
			PlanSummary: reversejob.PlanSummary{ImportCount: 361},
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--request", requestPath, "--out-dir", outputDir}, &stdout, &stderr, run)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (partial is a usable success); stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "partial") || !strings.Contains(stdout.String(), "361 imported") {
		t.Fatalf("stdout = %q, want a partial success summary mentioning the import count", stdout.String())
	}
	// The partial result.json the engine wrote must survive — mars must not
	// rewrite it as a failure result.
	raw, err := os.ReadFile(filepath.Join(outputDir, resultFile))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result reversejob.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if result.Status != reversejob.StatusPartial {
		t.Fatalf("status = %q, want %q (mars must not overwrite a partial result)", result.Status, reversejob.StatusPartial)
	}
}

func TestMainRejectsInsideOutImportedMarkerBeforePlan(t *testing.T) {
	useFakeDiscoverer(t)
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	outputDir := filepath.Join(dir, "out")
	req := reversejob.Request{
		Version: reversejob.Version,
		Resources: []reversejob.ResourceSpec{{
			Identity: imported.ResourceIdentity{
				Cloud:    "aws",
				Type:     "aws_s3_bucket",
				Address:  "aws_s3_bucket.example",
				Region:   "us-west-2",
				ImportID: "example-bucket",
				Tags: map[string]string{
					"InsideOutImported":      "true",
					"InsideOutImportProject": "4b982735-ff89-4295-a3fa-8a75a554ffc9",
				},
			},
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"--request", requestPath,
		"--out-dir", outputDir,
		"--import-project-id", "io-current",
	}, &stdout, &stderr, nil)

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr.String(), "selected resource cannot be imported") {
		t.Fatalf("stderr = %q, want unimportable selection rejection", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(outputDir, "imported.tf")); !os.IsNotExist(err) {
		t.Fatalf("imported.tf should not be written before rejection, stat err=%v", err)
	}
	raw, err := os.ReadFile(filepath.Join(outputDir, resultFile))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result reversejob.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if result.Status != reversejob.StatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, reversejob.StatusFailed)
	}
	if len(result.ValidationIssues) != 1 || result.ValidationIssues[0].Code != imported.ReasonInsideOutImported {
		t.Fatalf("validation issues = %#v, want insideout_imported", result.ValidationIssues)
	}
}

func TestMainPreservesExistingFailureResult(t *testing.T) {
	useFakeDiscoverer(t)
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
