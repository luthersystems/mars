// Package preflight implements the InsideOut bootstrap-permission preflights —
// a defense-in-depth guard for luthersystems/reliable#2243 (the Beatloom
// incident): a customer's under-privileged connecting credential passed
// credential validation and reached the cloud-provision Terraform apply, then
// 403'd mid-apply on a create action (storage.buckets.create /
// iam.serviceAccounts.create on GCP; s3:CreateBucket / iam:CreateRole on AWS) —
// AFTER the GitHub half of that apply had already created a repo, deploy key,
// and Actions variables, leaving orphaned partial state.
//
// This is the Go port of the sandbox-infrastructure-template shell preflights
// (tf/gcp-preflight.sh, tf/aws-preflight.sh). Those scripts encode the reviewed
// semantics; this binary reproduces them EXACTLY, replacing shell + jq + gcloud
// + curl + aws-cli string matching with typed cloud-SDK calls and typed error
// classification. The template retains ownership of the hook, the
// SKIP_GCP_BOOTSTRAP_PREFLIGHT / SKIP_AWS_BOOTSTRAP_PREFLIGHT escape hatches,
// and the canonical permission/action lists — the binary takes those lists as
// INPUT and supplies only the mechanics.
//
// # CLI contract
//
// This contract is the interface the template swap PR codes against. It is
// stable; the output markers below match the shell scripts byte-for-byte so log
// tooling and the template tests keep working across the swap.
//
//	insideout-preflight gcp --project-id <PID> --credentials-file <path to SA JSON> --permissions p1,p2,... [--timeout 30s]
//	insideout-preflight aws --actions a1,a2,... [--role-arn <ARN> [--external-id <ID>]] [--region r] [--timeout 30s]
//
// Permissions/actions accept comma-separated values and/or repeatable flags
// (--permissions a,b --permissions c); at least one is required.
//
// # Exit codes (the whole contract)
//
//	0  passed OR fail-open (verdict could not be definitively obtained — a
//	   prominent WARNING explains why). The template hook treats 0 as non-fatal.
//	1  definitive fail-closed (missing permissions / actions, or a definitive
//	   bad-credential rejection). The template hook treats ONLY exit 1 as fatal.
//	2  usage error (bad flags, empty permission/action list, unreadable
//	   credentials file).
//
// # GCP semantics
//
// Calls cloudresourcemanager.projects.testIamPermissions with the supplied
// permission list.
//
//   - 200 with a missing subset            → fail-closed, listing the missing perms.
//   - Malformed-JSON SA key, or a
//     definitive token rejection
//     (invalid_grant / HTTP 401)           → fail-closed (bad-credential message).
//   - Unloadable key MATERIAL (bad PEM in
//     otherwise-valid service_account JSON) → fail-open (deferred to the token
//     fetch, which surfaces an untyped parse error; a deliberate, safe
//     divergence from the shell — see gcp.go newGCPChecker).
//   - HTTP 403 on the call itself, HTTP
//     5xx, network error, timeout          → fail-open.
//   - Non-service_account JSON type
//     (WIF / external_account)             → fail-open (mirrors the shell's
//     defensive choice).
//
// Owner-grant caveat: the cloud-provision stage grants roles/owner to the
// InsideOut management service account, and GCP only permits a caller holding
// Owner to grant Owner. testIamPermissions cannot verify that, so PASSING this
// preflight does not by itself guarantee the Owner grant will succeed — the
// fail-closed remediation says so and recommends roles/owner.
//
// # AWS semantics
//
// With --role-arn: STS AssumeRole (+ external id) using ambient credentials,
// then iam:SimulatePrincipalPolicy with PolicySourceArn = the role ARN,
// executed under the assumed-role session credentials. Without --role-arn:
// resolve the caller via sts:GetCallerIdentity and map an assumed-role SESSION
// ARN back to the role IDENTITY ARN simulate requires (aws / aws-us-gov / aws-cn
// partitions; federated / root → fail-open).
//
//   - Simulate returns any non-"allowed"
//     required action                      → fail-closed, listing the denied actions.
//   - AssumeRole explicit AccessDenied     → fail-closed (distinct trust-policy /
//     external-id message).
//   - Any other AssumeRole error
//     (throttle, 5xx, network, expired
//     ambient creds)                       → fail-open.
//   - ANY SimulatePrincipalPolicy error
//     (incl. AccessDenied on
//     iam:SimulatePrincipalPolicy)         → fail-open (inability to verify is not
//     proof of insufficiency).
//
// Only EvalDecision == "allowed" counts; the SDK paginator merges results across
// pages. Failure classification is by typed SDK error (smithy.APIError codes),
// not string matching.
//
// Credential material (SA key contents, session tokens) is never printed. Every
// cloud API call is bounded by --timeout (default 30s).
package preflight
