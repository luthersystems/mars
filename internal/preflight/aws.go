package preflight

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithy "github.com/aws/smithy-go"
)

// stsAPI is the minimal STS surface the preflight needs. Small,
// package-owned interfaces let tests inject fakes without a live account.
type stsAPI interface {
	AssumeRole(ctx context.Context, in *sts.AssumeRoleInput, optFns ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// iamAPI is the minimal IAM surface. Its single method matches
// iam.SimulatePrincipalPolicyAPIClient, so a value of this type can drive the
// SDK paginator directly.
type iamAPI interface {
	SimulatePrincipalPolicy(ctx context.Context, in *iam.SimulatePrincipalPolicyInput, optFns ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error)
}

// awsSessionCreds carries assumed-role session credentials. A nil pointer means
// "use ambient credentials".
type awsSessionCreds struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// newAWSClients builds an STS + IAM client pair for the region. creds==nil uses
// ambient credentials (LoadDefaultConfig); a non-nil creds pins static
// assumed-role session credentials. Package var so tests can inject fakes.
var newAWSClients = defaultAWSClients

func defaultAWSClients(ctx context.Context, region string, creds *awsSessionCreds) (stsAPI, iamAPI, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if creds != nil {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	return sts.NewFromConfig(cfg), iam.NewFromConfig(cfg), nil
}

// ARN shapes for caller-identity → policy-source resolution. Partitions cover
// aws, aws-us-gov and aws-cn (the `[a-z-]+` partition token).
var (
	assumedRoleARNRe = regexp.MustCompile(`^arn:([a-z-]+):sts::([0-9]+):assumed-role/([^/]+)/`)
	iamIdentityARNRe = regexp.MustCompile(`^arn:[a-z-]+:iam::[0-9]+:(?:user|role)/`)
)

// mapCallerARNToPolicySource maps a caller identity ARN to the IAM identity ARN
// iam:SimulatePrincipalPolicy requires. An assumed-role SESSION ARN is rewritten
// to its role IDENTITY ARN; an IAM user/role identity ARN passes through. A
// federated-user or root ARN (or anything unrecognized) returns ok=false, which
// the caller treats as fail-open.
func mapCallerARNToPolicySource(arn string) (string, bool) {
	if m := assumedRoleARNRe.FindStringSubmatch(arn); m != nil {
		return fmt.Sprintf("arn:%s:iam::%s:role/%s", m[1], m[2], m[3]), true
	}
	if iamIdentityARNRe.MatchString(arn) {
		return arn, true
	}
	return "", false
}

// runAWS parses the aws subcommand flags and runs the AWS bootstrap-permission
// preflight, returning the process exit code.
func runAWS(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	l := logger{prefix: awsPrefix, out: stdout, err: stderr}

	fs := flag.NewFlagSet("insideout-preflight aws", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		roleARN    = fs.String("role-arn", "", "customer bootstrap role ARN to assume and evaluate (empty ⇒ ambient caller identity)")
		externalID = fs.String("external-id", "", "external id for the assume-role (confused-deputy protection)")
		region     = fs.String("region", "us-east-1", "AWS region for the STS/IAM control-plane calls")
		timeout    = fs.Duration("timeout", 30*time.Second, "per-call timeout")
		actions    stringList
	)
	fs.Var(&actions, "actions", "required bootstrap IAM actions (comma-separated and/or repeatable)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	if len(actions) == 0 {
		fprintln(stderr, "insideout-preflight aws: --actions must not be empty")
		return exitUsage
	}
	reg := strings.TrimSpace(*region)
	if reg == "" {
		reg = "us-east-1"
	}

	if strings.TrimSpace(*roleARN) != "" {
		return l.runAWSRoleMode(ctx, *roleARN, *externalID, reg, *timeout, actions)
	}
	return l.runAWSAmbientMode(ctx, reg, *timeout, actions)
}

// runAWSRoleMode assumes the customer bootstrap role, then simulates the actions
// under the assumed-role session credentials.
func (l logger) runAWSRoleMode(ctx context.Context, roleARN, externalID, region string, timeout time.Duration, actions []string) int {
	extNote := ""
	if externalID != "" {
		extNote = " +external-id"
	}
	l.outln(fmt.Sprintf("checking bootstrap actions against role %s (assume-role%s)", roleARN, extNote))

	stsClient, _, err := newAWSClients(ctx, region, nil)
	if err != nil {
		return l.failOpen("could not initialize AWS clients (transient): "+err.Error(), skipAWS)
	}

	assumeCtx, cancel := context.WithTimeout(ctx, timeout)
	in := &sts.AssumeRoleInput{
		RoleArn:         aws.String(roleARN),
		RoleSessionName: aws.String("insideout-bootstrap-preflight"),
		DurationSeconds: aws.Int32(900),
	}
	if externalID != "" {
		in.ExternalId = aws.String(externalID)
	}
	out, err := stsClient.AssumeRole(assumeCtx, in)
	cancel()
	if err != nil {
		return l.classifyAssumeFailure(roleARN, err)
	}
	if out == nil || out.Credentials == nil ||
		aws.ToString(out.Credentials.AccessKeyId) == "" ||
		aws.ToString(out.Credentials.SecretAccessKey) == "" ||
		aws.ToString(out.Credentials.SessionToken) == "" {
		return l.failOpen(fmt.Sprintf("assume-role for %s returned an incomplete credential set (treating as infra/transient).", roleARN), skipAWS)
	}

	creds := &awsSessionCreds{
		AccessKeyID:     aws.ToString(out.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(out.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(out.Credentials.SessionToken),
	}
	_, iamClient, err := newAWSClients(ctx, region, creds)
	if err != nil {
		return l.failOpen("could not initialize IAM client under the assumed role (transient): "+err.Error(), skipAWS)
	}
	// SimulatePrincipalPolicy takes the role's stable identity ARN — which the
	// caller-supplied bootstrap role ARN already is.
	return l.simulateAndReport(ctx, iamClient, roleARN, actions, timeout)
}

// runAWSAmbientMode resolves the ambient caller identity and simulates the
// actions against it.
func (l logger) runAWSAmbientMode(ctx context.Context, region string, timeout time.Duration, actions []string) int {
	stsClient, iamClient, err := newAWSClients(ctx, region, nil)
	if err != nil {
		return l.failOpen("could not initialize AWS clients (transient): "+err.Error(), skipAWS)
	}

	idCtx, cancel := context.WithTimeout(ctx, timeout)
	id, err := stsClient.GetCallerIdentity(idCtx, &sts.GetCallerIdentityInput{})
	cancel()
	if err != nil {
		return l.failOpen("sts:GetCallerIdentity failed (no usable ambient credentials / transient): "+err.Error(), skipAWS)
	}
	callerARN := aws.ToString(id.Arn)
	policySource, ok := mapCallerARNToPolicySource(callerARN)
	if !ok {
		return l.failOpen(fmt.Sprintf("caller identity %s is not an IAM user/role (federated/root?) — cannot resolve a simulate policy source.", callerARN), skipAWS)
	}
	l.outln("checking bootstrap actions against ambient caller identity " + policySource)
	return l.simulateAndReport(ctx, iamClient, policySource, actions, timeout)
}

// classifyAssumeFailure decides fail-closed vs fail-open for a failed
// sts:AssumeRole. An explicit AccessDenied (typed smithy.APIError code) is a
// trust-policy / external-id problem that WILL fail the deploy → fail-closed
// with a distinct, actionable message. Everything else (throttle, 5xx, network,
// expired ambient creds) is not evidence of insufficiency → fail-open.
func (l logger) classifyAssumeFailure(roleARN string, err error) int {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "AccessDenied" {
		return l.awsFailAssumeDenied(roleARN, err.Error())
	}
	return l.failOpen(fmt.Sprintf("could not assume bootstrap role %s (transient — throttling/5xx/network/expired creds): %s", roleARN, err.Error()), skipAWS)
}

// simulateAndReport runs iam:SimulatePrincipalPolicy (paginated) for the actions
// and reports the verdict. ANY simulate error is fail-open (inability to verify
// is not proof of insufficiency); only a successful simulate that leaves a
// required action non-"allowed" is fail-closed.
func (l logger) simulateAndReport(ctx context.Context, iamClient iamAPI, policySource string, actions []string, timeout time.Duration) int {
	simCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pag := iam.NewSimulatePrincipalPolicyPaginator(iamClient, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(policySource),
		ActionNames:     actions,
	})
	allowed := make(map[string]bool, len(actions))
	for pag.HasMorePages() {
		page, err := pag.NextPage(simCtx)
		if err != nil {
			return l.failOpen("iam:SimulatePrincipalPolicy call failed (cannot verify — the principal may lack iam:SimulatePrincipalPolicy, or a transient AWS error): "+err.Error(), skipAWS)
		}
		for _, ev := range page.EvaluationResults {
			// Only an exact "allowed" decision counts; explicitDeny / implicitDeny
			// / anything else is treated as denied.
			if string(ev.EvalDecision) == "allowed" {
				allowed[aws.ToString(ev.EvalActionName)] = true
			}
		}
	}

	var denied []string
	for _, a := range actions {
		if !allowed[a] {
			denied = append(denied, a)
		}
	}
	if len(denied) == 0 {
		l.outln(fmt.Sprintf("OK — principal %s is allowed all %d bootstrap actions.", policySource, len(actions)))
		return exitOK
	}
	return l.awsFailMissing(policySource, denied, len(actions))
}

// awsFailAssumeDenied prints the assume-role AccessDenied verdict (fail-closed,
// exit 1) — a trust-policy / external-id problem, distinct from a missing-action
// verdict. Matches tf/aws-preflight.sh byte-for-byte.
func (l logger) awsFailAssumeDenied(roleARN, detail string) int {
	l.errln("")
	l.errln("================================================================")
	l.errln("AWS BOOTSTRAP PREFLIGHT FAILED — could not assume bootstrap role")
	l.errln(fmt.Sprintf("%s: the assume-role call was explicitly DENIED (AccessDenied).", roleARN))
	l.errln("")
	l.errln("This is a TRUST-POLICY or EXTERNAL-ID problem, not a missing deploy")
	l.errln("permission. Fix on the customer side:")
	l.errln("  - the role's trust policy must allow the connecting deployer principal")
	l.errln("    to sts:AssumeRole it, and")
	l.errln("  - the external id must match the one configured for this deployment")
	l.errln("    (aws_external_id).")
	if strings.TrimSpace(detail) != "" {
		l.errln(detail)
	}
	l.errln("")
	l.errln("Escape hatch: set SKIP_AWS_BOOTSTRAP_PREFLIGHT=1 to bypass this check.")
	l.errln("Ref: luthersystems/reliable#2243.")
	l.errln("================================================================")
	return exitFail
}

// awsFailMissing prints the definitive denied-action verdict (fail-closed, exit
// 1). totalActions is the full required-action count for the remediation line.
// Matches tf/aws-preflight.sh byte-for-byte.
func (l logger) awsFailMissing(policySource string, denied []string, totalActions int) int {
	l.errln("")
	l.errln("================================================================")
	l.errln("AWS BOOTSTRAP PREFLIGHT FAILED — the principal")
	l.errln(fmt.Sprintf("%s is missing %d required IAM action(s):", policySource, len(denied)))
	for _, action := range denied {
		l.errln("  - " + action)
	}
	l.errln("")
	l.errln("Attach the AdministratorAccess managed policy to this principal, or a")
	l.errln(fmt.Sprintf("policy granting at minimum the %d bootstrap actions the", totalActions))
	l.errln("cloud-provision stage needs (the denied ones are listed above), then re-run")
	l.errln("the deploy.")
	l.errln("")
	l.errln("Escape hatch: set SKIP_AWS_BOOTSTRAP_PREFLIGHT=1 to bypass this check.")
	l.errln("Ref: luthersystems/reliable#2243.")
	l.errln("================================================================")
	return exitFail
}
