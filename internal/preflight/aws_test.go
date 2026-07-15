package preflight

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	smithy "github.com/aws/smithy-go"
)

// --- fakes ------------------------------------------------------------------

type fakeSTS struct {
	assumeOut   *sts.AssumeRoleOutput
	assumeErr   error
	callerOut   *sts.GetCallerIdentityOutput
	callerErr   error
	assumeCalls int
	callerCalls int
}

func (f *fakeSTS) AssumeRole(_ context.Context, _ *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.assumeCalls++
	return f.assumeOut, f.assumeErr
}

func (f *fakeSTS) GetCallerIdentity(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	f.callerCalls++
	return f.callerOut, f.callerErr
}

type fakeIAM struct {
	pages []*iam.SimulatePrincipalPolicyOutput // returned in order, one per call
	err   error
	calls int
}

func (f *fakeIAM) SimulatePrincipalPolicy(_ context.Context, _ *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	idx := f.calls
	f.calls++
	if idx >= len(f.pages) {
		return &iam.SimulatePrincipalPolicyOutput{}, nil
	}
	return f.pages[idx], nil
}

// withFakeAWSClients installs fakes for the duration of the test.
func withFakeAWSClients(t *testing.T, s *fakeSTS, i *fakeIAM) {
	t.Helper()
	orig := newAWSClients
	newAWSClients = func(_ context.Context, _ string, _ *awsSessionCreds) (stsAPI, iamAPI, error) {
		return s, i, nil
	}
	t.Cleanup(func() { newAWSClients = orig })
}

// evalPage builds one SimulatePrincipalPolicy page: each action in allowed is
// marked "allowed"; every other action passed is marked implicitDeny. marker,
// when non-empty, sets the pagination Marker so the paginator fetches again.
func evalPage(marker string, actions []string, allowed ...string) *iam.SimulatePrincipalPolicyOutput {
	allow := map[string]bool{}
	for _, a := range allowed {
		allow[a] = true
	}
	var results []iamtypes.EvaluationResult
	for _, a := range actions {
		decision := "implicitDeny"
		if allow[a] {
			decision = "allowed"
		}
		results = append(results, iamtypes.EvaluationResult{
			EvalActionName: aws.String(a),
			EvalDecision:   iamtypes.PolicyEvaluationDecisionType(decision),
		})
	}
	out := &iam.SimulatePrincipalPolicyOutput{EvaluationResults: results}
	if marker != "" {
		out.IsTruncated = true
		out.Marker = aws.String(marker)
	}
	return out
}

func assumeCreds() *sts.AssumeRoleOutput {
	return &sts.AssumeRoleOutput{
		Credentials: &ststypes.Credentials{
			AccessKeyId:     aws.String("AKIA_TEST"),
			SecretAccessKey: aws.String("secret"),
			SessionToken:    aws.String("token"),
		},
	}
}

func apiErr(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code + " message"}
}

const testRole = "arn:aws:iam::123456789012:role/insideout-bootstrap"

// --- tests ------------------------------------------------------------------

func TestAWS_AllActionsAllowed_RoleMode(t *testing.T) {
	actions := []string{"s3:CreateBucket", "iam:CreateRole", "sts:AssumeRole"}
	withFakeAWSClients(t,
		&fakeSTS{assumeOut: assumeCreds()},
		&fakeIAM{pages: []*iam.SimulatePrincipalPolicyOutput{evalPage("", actions, actions...)}},
	)
	code, out, _ := runPreflight(t, "aws", "--role-arn", testRole, "--actions", strings.Join(actions, ","))
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "OK — principal") || !strings.Contains(out, "allowed all 3 bootstrap actions") {
		t.Errorf("missing OK summary; stdout=%q", out)
	}
}

func TestAWS_ImplicitDenySubset_FailClosed(t *testing.T) {
	actions := []string{"s3:CreateBucket", "iam:CreateRole", "sts:AssumeRole"}
	// Everything allowed except the two representative create actions.
	withFakeAWSClients(t,
		&fakeSTS{assumeOut: assumeCreds()},
		&fakeIAM{pages: []*iam.SimulatePrincipalPolicyOutput{evalPage("", actions, "sts:AssumeRole")}},
	)
	code, _, errOut := runPreflight(t, "aws", "--role-arn", testRole, "--actions", strings.Join(actions, ","))
	if code != exitFail {
		t.Fatalf("exit = %d, want %d", code, exitFail)
	}
	for _, want := range []string{
		"AWS BOOTSTRAP PREFLIGHT FAILED — the principal",
		"s3:CreateBucket",
		"iam:CreateRole",
		"AdministratorAccess",
		"reliable#2243",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, errOut)
		}
	}
	if got := countPrefixLines(errOut, awsPrefix+"  - "); got != 2 {
		t.Errorf("denied listing lines = %d, want 2; stderr=\n%s", got, errOut)
	}
	// The granted action must NOT surface as denied.
	if strings.Contains(errOut, awsPrefix+"  - sts:AssumeRole") {
		t.Errorf("granted action sts:AssumeRole must not appear in the denied listing")
	}
}

func TestAWS_SimulateAccessDenied_FailOpen(t *testing.T) {
	actions := []string{"s3:CreateBucket"}
	withFakeAWSClients(t,
		&fakeSTS{assumeOut: assumeCreds()},
		&fakeIAM{err: apiErr("AccessDenied")}, // AccessDenied on iam:SimulatePrincipalPolicy itself
	)
	code, _, errOut := runPreflight(t, "aws", "--role-arn", testRole, "--actions", strings.Join(actions, ","))
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open)", code, exitOK)
	}
	if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "SimulatePrincipalPolicy call failed") {
		t.Errorf("stderr missing fail-open warning; got:\n%s", errOut)
	}
}

func TestAWS_AssumeRoleAccessDenied_FailClosedDistinct(t *testing.T) {
	withFakeAWSClients(t,
		&fakeSTS{assumeErr: apiErr("AccessDenied")},
		&fakeIAM{},
	)
	code, _, errOut := runPreflight(t, "aws", "--role-arn", testRole, "--external-id", "xid", "--actions", "s3:CreateBucket")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d", code, exitFail)
	}
	if !strings.Contains(errOut, "could not assume bootstrap role") || !strings.Contains(errOut, "TRUST-POLICY") {
		t.Errorf("stderr missing distinct assume-denied message; got:\n%s", errOut)
	}
	// Must NOT be conflated with the missing-action verdict.
	if strings.Contains(errOut, "required IAM action") {
		t.Errorf("assume-denied must not be reported as a missing-action verdict; got:\n%s", errOut)
	}
}

func TestAWS_AssumeRoleThrottle_FailOpen(t *testing.T) {
	withFakeAWSClients(t,
		&fakeSTS{assumeErr: apiErr("Throttling")},
		&fakeIAM{},
	)
	code, _, errOut := runPreflight(t, "aws", "--role-arn", testRole, "--actions", "s3:CreateBucket")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open)", code, exitOK)
	}
	if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "transient") {
		t.Errorf("stderr missing transient fail-open warning; got:\n%s", errOut)
	}
}

func TestAWS_PaginationMergesTwoPages(t *testing.T) {
	actions := []string{"a1", "a2", "a3", "a4", "a5"}
	iamC := &fakeIAM{pages: []*iam.SimulatePrincipalPolicyOutput{
		evalPage("page2", actions, "a1", "a2"),  // page 1: a1,a2 allowed, Marker set
		evalPage("", actions, "a3", "a4", "a5"), // page 2: a3,a4,a5 allowed, no Marker
	}}
	withFakeAWSClients(t, &fakeSTS{assumeOut: assumeCreds()}, iamC)
	code, out, errOut := runPreflight(t, "aws", "--role-arn", testRole, "--actions", strings.Join(actions, ","))
	if code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr=\n%s", code, exitOK, errOut)
	}
	if iamC.calls != 2 {
		t.Errorf("simulate calls = %d, want 2 (paginator must fetch both pages)", iamC.calls)
	}
	if !strings.Contains(out, "allowed all 5 bootstrap actions") {
		t.Errorf("expected all-allowed summary after merging pages; stdout=%q", out)
	}
}

func TestAWS_AmbientMode_SessionARNMapped(t *testing.T) {
	actions := []string{"s3:CreateBucket"}
	withFakeAWSClients(t,
		&fakeSTS{callerOut: &sts.GetCallerIdentityOutput{
			Arn: aws.String("arn:aws:sts::123456789012:assumed-role/MyRole/session-xyz"),
		}},
		&fakeIAM{pages: []*iam.SimulatePrincipalPolicyOutput{evalPage("", actions, actions...)}},
	)
	code, out, errOut := runPreflight(t, "aws", "--actions", strings.Join(actions, ","))
	if code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr=\n%s", code, exitOK, errOut)
	}
	// Session ARN must be mapped back to the role identity ARN for simulate.
	if !strings.Contains(out, "ambient caller identity arn:aws:iam::123456789012:role/MyRole") {
		t.Errorf("caller session ARN was not mapped to the role identity ARN; stdout=%q", out)
	}
}

func TestAWS_AmbientMode_GetCallerIdentityError_FailOpen(t *testing.T) {
	withFakeAWSClients(t,
		&fakeSTS{callerErr: apiErr("ExpiredToken")},
		&fakeIAM{},
	)
	code, _, errOut := runPreflight(t, "aws", "--actions", "s3:CreateBucket")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open)", code, exitOK)
	}
	if !strings.Contains(errOut, "GetCallerIdentity failed") {
		t.Errorf("stderr missing GetCallerIdentity fail-open warning; got:\n%s", errOut)
	}
}

func TestAWS_IncompleteAssumeCreds_FailOpen(t *testing.T) {
	withFakeAWSClients(t,
		&fakeSTS{assumeOut: &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
			AccessKeyId: aws.String("AKIA"), // missing secret + token
		}}},
		&fakeIAM{},
	)
	code, _, errOut := runPreflight(t, "aws", "--role-arn", testRole, "--actions", "s3:CreateBucket")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open)", code, exitOK)
	}
	if !strings.Contains(errOut, "incomplete credential set") {
		t.Errorf("stderr missing incomplete-creds fail-open warning; got:\n%s", errOut)
	}
}

func TestMapCallerARNToPolicySource(t *testing.T) {
	cases := []struct {
		name   string
		arn    string
		want   string
		wantOK bool
	}{
		{"aws assumed-role session", "arn:aws:sts::123456789012:assumed-role/MyRole/sess", "arn:aws:iam::123456789012:role/MyRole", true},
		{"aws-us-gov assumed-role", "arn:aws-us-gov:sts::210987654321:assumed-role/GovRole/sess", "arn:aws-us-gov:iam::210987654321:role/GovRole", true},
		{"aws-cn assumed-role", "arn:aws-cn:sts::111122223333:assumed-role/CnRole/sess", "arn:aws-cn:iam::111122223333:role/CnRole", true},
		{"direct iam role passthrough", "arn:aws:iam::123456789012:role/Direct", "arn:aws:iam::123456789012:role/Direct", true},
		{"direct iam user passthrough", "arn:aws:iam::123456789012:user/alice", "arn:aws:iam::123456789012:user/alice", true},
		{"aws-cn iam user passthrough", "arn:aws-cn:iam::111122223333:user/bob", "arn:aws-cn:iam::111122223333:user/bob", true},
		{"root fails open", "arn:aws:iam::123456789012:root", "", false},
		{"federated-user fails open", "arn:aws:sts::123456789012:federated-user/fed", "", false},
		{"garbage fails open", "not-an-arn", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mapCallerARNToPolicySource(tc.arn)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("mapCallerARNToPolicySource(%q) = (%q, %v), want (%q, %v)", tc.arn, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// runPreflight drives Main end-to-end and captures the split output streams.
func runPreflight(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

// countPrefixLines counts lines beginning with prefix.
func countPrefixLines(s, prefix string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}
