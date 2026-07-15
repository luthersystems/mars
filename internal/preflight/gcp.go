package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// gcpChecker runs projects.testIamPermissions for the supplied permission list
// and returns the granted subset. It isolates the single live GCP call so tests
// can point it at an httptest endpoint.
type gcpChecker func(ctx context.Context, projectID string, perms []string) (granted []string, err error)

// newGCPChecker builds the production checker from a service-account key. It is a
// package var so tests can substitute an httptest-backed or synthetic checker.
//
// A structural construction failure — JSON that clears classifyGCPCredential's
// lenient {type,client_email} parse yet fails CredentialsFromJSONWithType's full
// decode (e.g. a wrong-typed field) — is a definitive bad credential, wrapped in
// *gcpBadCredentialError so runGCP fails closed. Note this does NOT include
// garbage key MATERIAL: CredentialsFromJSONWithType builds a lazy JWT token
// source and never parses the private_key PEM, so an unloadable key does not
// error here — it surfaces on the first token fetch inside the checker call and
// is classified by classifyGCPAPIError (token-endpoint rejection → fail-closed;
// a locally-unparseable PEM → fail-open). That is a deliberate, safe divergence
// from the shell's gcloud-activate fail-closed: such a key is already rejected
// upstream (Oracle authenticates it at credential-validate; setupGCPCredentials
// re-checks .type), and even if one reached here terraform would fail at plan
// before creating anything. A client-construction failure is infra (fail-open).
var newGCPChecker = defaultGCPChecker

func defaultGCPChecker(ctx context.Context, saJSON []byte) (gcpChecker, error) {
	// CredentialsFromJSONWithType is the non-deprecated form: it asserts the
	// JSON is a service-account key and applies the cloud-platform scope. Only a
	// structural decode failure errors here (→ definitive bad credential);
	// garbage key material is deferred to the first token fetch (see above).
	creds, err := google.CredentialsFromJSONWithType(ctx, saJSON, google.ServiceAccount, cloudresourcemanager.CloudPlatformScope)
	if err != nil {
		return nil, &gcpBadCredentialError{err: err}
	}
	svc, err := cloudresourcemanager.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, projectID string, perms []string) ([]string, error) {
		resp, err := svc.Projects.TestIamPermissions(projectID, &cloudresourcemanager.TestIamPermissionsRequest{
			Permissions: perms,
		}).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		return resp.Permissions, nil
	}, nil
}

// gcpBadCredentialError marks a definitive construction-time bad-credential
// failure (a structural decode failure in CredentialsFromJSONWithType) so runGCP
// fails closed rather than fail-open. Garbage key MATERIAL is not detected at
// construction — see newGCPChecker and classifyGCPAPIError.
type gcpBadCredentialError struct{ err error }

func (e *gcpBadCredentialError) Error() string { return e.err.Error() }
func (e *gcpBadCredentialError) Unwrap() error { return e.err }

// gcpCredKind classifies a supplied credential file's content BEFORE any network
// call, mirroring the shell's jq-based `.type` inspection.
type gcpCredKind int

const (
	gcpCredOK             gcpCredKind = iota // valid service_account JSON
	gcpCredMalformed                         // not parseable as JSON → fail-closed
	gcpCredNotServiceAcct                    // parses, but type != service_account → fail-open
)

// gcpCredInfo carries the fields the messages need.
type gcpCredInfo struct {
	clientEmail string
	credType    string
}

// classifyGCPCredential inspects the raw SA key JSON. It never makes a network
// call. Malformed JSON is a definitive bad credential (fail-closed); a parseable
// JSON whose type is not service_account (e.g. WIF/external_account) is
// fail-open, matching the shell's defensive choice.
func classifyGCPCredential(raw []byte) (gcpCredInfo, gcpCredKind) {
	var doc struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return gcpCredInfo{}, gcpCredMalformed
	}
	info := gcpCredInfo{clientEmail: doc.ClientEmail, credType: doc.Type}
	if doc.Type != "service_account" {
		return info, gcpCredNotServiceAcct
	}
	return info, gcpCredOK
}

// classifyGCPAPIError decides fail-closed vs fail-open for an error returned by
// the testIamPermissions call. It prefers typed classification (oauth2 token
// rejection, googleapi HTTP status) over string matching; the string fallback
// is a safety net for opaque token-endpoint wrappers.
//
//   - oauth2 token rejection / HTTP 401  → fail-closed (definitive bad credential).
//   - HTTP 403 / 5xx / other non-200     → fail-open (not a permission verdict).
//   - context deadline / cancellation    → fail-open.
//   - anything else (network/transport)  → fail-open.
func classifyGCPAPIError(err error) (failClosed bool, detail string) {
	if err == nil {
		return false, ""
	}
	// A token-exchange rejection (revoked/deleted SA, invalid_grant) surfaces as
	// *oauth2.RetrieveError — a definitive bad-credential verdict.
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return true, strings.TrimSpace(re.Error())
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case 401:
			return true, strings.TrimSpace(gerr.Error())
		case 403:
			return false, "testIamPermissions returned HTTP 403 (not a permission verdict; treating as infra/transient)."
		default:
			return false, fmt.Sprintf("testIamPermissions returned HTTP %d (not a permission verdict; treating as infra/transient).", gerr.Code)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false, "testIamPermissions call timed out (treating as infra/transient)."
	}
	// Safety net: definitive OAuth token rejections occasionally arrive as opaque
	// errors that do not unwrap to *oauth2.RetrieveError. Mirror the shell's grep
	// for the canonical token-endpoint error codes.
	low := strings.ToLower(err.Error())
	for _, tok := range []string{"invalid_grant", "invalid_client", "unauthorized_client"} {
		if strings.Contains(low, tok) {
			return true, strings.TrimSpace(err.Error())
		}
	}
	return false, "testIamPermissions call failed (network/transport error): " + err.Error()
}

// runGCP parses the gcp subcommand flags and runs the GCP bootstrap-permission
// preflight, returning the process exit code.
func runGCP(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	l := logger{prefix: gcpPrefix, out: stdout, err: stderr}

	fs := flag.NewFlagSet("insideout-preflight gcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		projectID = fs.String("project-id", "", "GCP project id to check bootstrap permissions on")
		credsFile = fs.String("credentials-file", "", "path to the service-account key JSON")
		timeout   = fs.Duration("timeout", 30*time.Second, "per-call timeout")
		perms     stringList
	)
	fs.Var(&perms, "permissions", "required bootstrap permissions (comma-separated and/or repeatable)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	if strings.TrimSpace(*projectID) == "" {
		fprintln(stderr, "insideout-preflight gcp: --project-id is required")
		return exitUsage
	}
	if strings.TrimSpace(*credsFile) == "" {
		fprintln(stderr, "insideout-preflight gcp: --credentials-file is required")
		return exitUsage
	}
	if len(perms) == 0 {
		fprintln(stderr, "insideout-preflight gcp: --permissions must not be empty")
		return exitUsage
	}

	raw, err := os.ReadFile(*credsFile)
	if err != nil {
		// An unreadable credentials FILE is a usage error, distinct from an
		// unreadable-KEY (malformed content), which is a fail-closed verdict.
		fprintf(stderr, "insideout-preflight gcp: cannot read --credentials-file %q: %v\n", *credsFile, err)
		return exitUsage
	}

	info, kind := classifyGCPCredential(raw)
	switch kind {
	case gcpCredMalformed:
		return l.gcpFailLoadKey("unknown", "the service account key JSON is not parseable")
	case gcpCredNotServiceAcct:
		return l.failOpen(fmt.Sprintf("credential type '%s' is not 'service_account' (WIF/external_account?) — skipping token mint.", info.credType), skipGCP)
	}

	saEmail := info.clientEmail
	if saEmail == "" {
		saEmail = "unknown"
	}
	l.outln(fmt.Sprintf("checking bootstrap permissions for service account %s on project %s", saEmail, *projectID))

	checker, err := newGCPChecker(ctx, raw)
	if err != nil {
		var bad *gcpBadCredentialError
		if errors.As(err, &bad) {
			return l.gcpFailLoadKey(saEmail, bad.Error())
		}
		return l.failOpen("could not initialize cloudresourcemanager client: "+err.Error(), skipGCP)
	}

	callCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	granted, err := checker(callCtx, *projectID, perms)
	if err != nil {
		failClosed, detail := classifyGCPAPIError(err)
		if failClosed {
			return l.gcpFailTokenRejected(saEmail, detail)
		}
		return l.failOpen(detail, skipGCP)
	}

	missing := missingItems(perms, granted)
	if len(missing) == 0 {
		l.outln(fmt.Sprintf("OK — service account %s holds all %d bootstrap permissions on project %s.", saEmail, len(perms), *projectID))
		return exitOK
	}
	return l.gcpFailMissing(saEmail, *projectID, missing)
}

// missingItems returns the required items absent from the granted set, in the
// required-list order.
func missingItems(required, granted []string) []string {
	have := make(map[string]bool, len(granted))
	for _, g := range granted {
		have[g] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// gcpFailMissing prints the definitive missing-permission verdict (fail-closed,
// exit 1). The block matches tf/gcp-preflight.sh byte-for-byte, including the
// roles/owner Owner-grant caveat.
func (l logger) gcpFailMissing(saEmail, projectID string, missing []string) int {
	l.errln("")
	l.errln("================================================================")
	l.errln("GCP BOOTSTRAP PREFLIGHT FAILED — the provided service account")
	l.errln(fmt.Sprintf("%s is missing %d required permission(s) on project %s:", saEmail, len(missing), projectID))
	for _, perm := range missing {
		l.errln("  - " + perm)
	}
	l.errln("")
	l.errln("Grant the service account roles/owner on the project (required anyway:")
	l.errln("this bootstrap stage grants Owner to the InsideOut management service")
	l.errln("account, which GCP only permits from a caller that itself holds Owner;")
	l.errln("testIamPermissions cannot verify that, so passing this preflight does not")
	l.errln("by itself guarantee the Owner grant will succeed).")
	l.errln("")
	l.errln("At minimum grant: roles/storage.admin + roles/iam.serviceAccountAdmin +")
	l.errln("roles/resourcemanager.projectIamAdmin — then re-run the deploy.")
	l.errln("")
	l.errln("Escape hatch: set SKIP_GCP_BOOTSTRAP_PREFLIGHT=1 to bypass this check.")
	l.errln("Ref: luthersystems/reliable#2243.")
	l.errln("================================================================")
	return exitFail
}

// gcpFailLoadKey prints the bad-key fail-closed verdict (a key that cannot be
// loaded — malformed / revoked / invalid material). Matches the shell's
// activate-service-account failure block.
func (l logger) gcpFailLoadKey(saEmail, detail string) int {
	l.errln("")
	l.errln("GCP BOOTSTRAP PREFLIGHT FAILED — could not load the service account key")
	l.errln(fmt.Sprintf("for %s: the key is malformed, revoked, or invalid.", saEmail))
	if strings.TrimSpace(detail) != "" {
		l.errln(detail)
	}
	l.errln("Ref: luthersystems/reliable#2243.")
	return exitFail
}

// gcpFailTokenRejected prints the token-rejection fail-closed verdict (bad /
// revoked key rejected at token exchange or with HTTP 401). Matches the shell's
// token-exchange rejection block.
func (l logger) gcpFailTokenRejected(saEmail, detail string) int {
	l.errln("")
	l.errln("GCP BOOTSTRAP PREFLIGHT FAILED — token exchange was REJECTED for")
	l.errln(fmt.Sprintf("%s (bad / revoked service account key):", saEmail))
	if strings.TrimSpace(detail) != "" {
		l.errln(detail)
	}
	l.errln("Ref: luthersystems/reliable#2243.")
	return exitFail
}
