package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// minimal service-account JSON that passes classifyGCPCredential (type check)
// without any real key material — the httptest-backed checker ignores it.
const testSAJSON = `{"type":"service_account","client_email":"sa@test.iam.gserviceaccount.com"}`

// testIamServer stands up a fake cloudresourcemanager testIamPermissions
// endpoint. handler receives the requested permissions and returns the HTTP
// status, the granted subset (for 200s), and an optional delay.
func testIamServer(t *testing.T, handler func(perms []string) (status int, granted []string, delay time.Duration)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":testIamPermissions") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var req struct {
			Permissions []string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		status, granted, delay := handler(req.Permissions)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(status) + `,"message":"boom"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"permissions": granted})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withGCPEndpoint points newGCPChecker at a fake endpoint using an
// unauthenticated real cloudresourcemanager client, exercising the real request
// build + response parse + classification path with no credentials.
func withGCPEndpoint(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := newGCPChecker
	newGCPChecker = func(ctx context.Context, _ []byte) (gcpChecker, error) {
		svc, err := cloudresourcemanager.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
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
	t.Cleanup(func() { newGCPChecker = orig })
}

// writeSAKey writes an SA-key file and returns its path.
func writeSAKey(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write sa key: %v", err)
	}
	return path
}

func TestGCP_AllPermissionsGranted(t *testing.T) {
	perms := []string{"storage.buckets.create", "iam.serviceAccounts.create", "resourcemanager.projects.get"}
	withGCPEndpoint(t, testIamServer(t, func(reqPerms []string) (int, []string, time.Duration) {
		return http.StatusOK, reqPerms, 0 // grant everything requested
	}))
	sa := writeSAKey(t, testSAJSON)
	code, out, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", strings.Join(perms, ","))
	if code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr=\n%s", code, exitOK, errOut)
	}
	if !strings.Contains(out, "holds all 3 bootstrap permissions") {
		t.Errorf("missing OK summary; stdout=%q", out)
	}
}

func TestGCP_MissingSubset_FailClosed(t *testing.T) {
	perms := []string{"storage.buckets.create", "iam.serviceAccounts.create", "resourcemanager.projects.get"}
	withGCPEndpoint(t, testIamServer(t, func(_ []string) (int, []string, time.Duration) {
		// grant only the read; withhold the two #2243 create permissions
		return http.StatusOK, []string{"resourcemanager.projects.get"}, 0
	}))
	sa := writeSAKey(t, testSAJSON)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", strings.Join(perms, ","))
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stderr=\n%s", code, exitFail, errOut)
	}
	for _, want := range []string{
		"GCP BOOTSTRAP PREFLIGHT FAILED — the provided service account",
		"storage.buckets.create",
		"iam.serviceAccounts.create",
		"roles/owner",
		"reliable#2243",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, errOut)
		}
	}
	if got := countPrefixLines(errOut, gcpPrefix+"  - "); got != 2 {
		t.Errorf("missing-permission listing lines = %d, want 2; stderr=\n%s", got, errOut)
	}
	if strings.Contains(errOut, gcpPrefix+"  - resourcemanager.projects.get") {
		t.Errorf("granted permission must not appear in the missing listing")
	}
}

func TestGCP_HTTP403_FailOpen(t *testing.T) {
	withGCPEndpoint(t, testIamServer(t, func(_ []string) (int, []string, time.Duration) {
		return http.StatusForbidden, nil, 0
	}))
	sa := writeSAKey(t, testSAJSON)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open); stderr=\n%s", code, exitOK, errOut)
	}
	if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "HTTP 403") {
		t.Errorf("stderr missing 403 fail-open warning; got:\n%s", errOut)
	}
}

func TestGCP_HTTP500_FailOpen(t *testing.T) {
	withGCPEndpoint(t, testIamServer(t, func(_ []string) (int, []string, time.Duration) {
		return http.StatusInternalServerError, nil, 0
	}))
	sa := writeSAKey(t, testSAJSON)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open); stderr=\n%s", code, exitOK, errOut)
	}
	if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "HTTP 500") {
		t.Errorf("stderr missing 500 fail-open warning; got:\n%s", errOut)
	}
}

func TestGCP_Timeout_FailOpen(t *testing.T) {
	withGCPEndpoint(t, testIamServer(t, func(reqPerms []string) (int, []string, time.Duration) {
		return http.StatusOK, reqPerms, 500 * time.Millisecond // exceed the --timeout below
	}))
	sa := writeSAKey(t, testSAJSON)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create", "--timeout", "100ms")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open); stderr=\n%s", code, exitOK, errOut)
	}
	if !strings.Contains(errOut, "WARNING") {
		t.Errorf("stderr missing timeout fail-open warning; got:\n%s", errOut)
	}
}

func TestGCP_MalformedKeyFile_FailClosed(t *testing.T) {
	// Unparseable JSON is a definitive bad credential — no network call.
	sa := writeSAKey(t, "not-json-at-all")
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stderr=\n%s", code, exitFail, errOut)
	}
	for _, want := range []string{"could not load the service account key", "malformed", "reliable#2243"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, errOut)
		}
	}
}

func TestGCP_NonServiceAccountType_FailOpen(t *testing.T) {
	sa := writeSAKey(t, `{"type":"external_account","audience":"//iam.googleapis.com/..."}`)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (fail-open); stderr=\n%s", code, exitOK, errOut)
	}
	if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "not 'service_account'") {
		t.Errorf("stderr missing non-service_account fail-open warning; got:\n%s", errOut)
	}
}

func TestGCP_BadCredentialFromFactory_FailClosed(t *testing.T) {
	orig := newGCPChecker
	newGCPChecker = func(context.Context, []byte) (gcpChecker, error) {
		return nil, &gcpBadCredentialError{err: errors.New("could not parse key: bad PEM")}
	}
	t.Cleanup(func() { newGCPChecker = orig })

	sa := writeSAKey(t, testSAJSON)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stderr=\n%s", code, exitFail, errOut)
	}
	if !strings.Contains(errOut, "could not load the service account key") {
		t.Errorf("stderr missing bad-key fail-closed block; got:\n%s", errOut)
	}
}

// TestMainRecoversPanic_FailOpen pins the panic contract: an internal panic in
// a subcommand is recovered and converted to fail-open (exit 0), never an
// uncaught non-zero process abort a hook might treat as fatal. Uses the
// newGCPChecker seam to inject a panic on the live-call path.
func TestMainRecoversPanic_FailOpen(t *testing.T) {
	orig := newGCPChecker
	newGCPChecker = func(context.Context, []byte) (gcpChecker, error) {
		panic("boom: simulated internal failure")
	}
	t.Cleanup(func() { newGCPChecker = orig })

	sa := writeSAKey(t, testSAJSON)
	code, _, errOut := runPreflight(t, "gcp", "--project-id", "p1", "--credentials-file", sa, "--permissions", "storage.buckets.create")
	if code != exitOK {
		t.Fatalf("panic path exit = %d, want %d (fail-open); stderr=\n%s", code, exitOK, errOut)
	}
	if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "internal error") {
		t.Errorf("stderr missing panic fail-open warning; got:\n%s", errOut)
	}
}

func TestClassifyGCPCredential(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantKind  gcpCredKind
		wantEmail string
		wantType  string
	}{
		{"valid service_account", `{"type":"service_account","client_email":"sa@x.iam"}`, gcpCredOK, "sa@x.iam", "service_account"},
		{"malformed json", `not-json`, gcpCredMalformed, "", ""},
		{"external_account", `{"type":"external_account"}`, gcpCredNotServiceAcct, "", "external_account"},
		{"empty type", `{"client_email":"sa@x.iam"}`, gcpCredNotServiceAcct, "sa@x.iam", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, kind := classifyGCPCredential([]byte(tc.raw))
			if kind != tc.wantKind {
				t.Errorf("kind = %v, want %v", kind, tc.wantKind)
			}
			if kind != gcpCredMalformed {
				if info.clientEmail != tc.wantEmail {
					t.Errorf("clientEmail = %q, want %q", info.clientEmail, tc.wantEmail)
				}
				if info.credType != tc.wantType {
					t.Errorf("credType = %q, want %q", info.credType, tc.wantType)
				}
			}
		})
	}
}

func TestClassifyGCPAPIError(t *testing.T) {
	tokenReject := &oauth2.RetrieveError{
		Response:  &http.Response{Status: "400 Bad Request", StatusCode: 400},
		Body:      []byte(`{"error":"invalid_grant"}`),
		ErrorCode: "invalid_grant",
	}
	cases := []struct {
		name           string
		err            error
		wantFailClosed bool
		wantDetailSub  string
	}{
		{"oauth2 token rejection", tokenReject, true, ""},
		{"googleapi 401", &googleapi.Error{Code: 401, Message: "unauthenticated"}, true, ""},
		{"googleapi 403", &googleapi.Error{Code: 403, Message: "denied"}, false, "HTTP 403"},
		{"googleapi 500", &googleapi.Error{Code: 500, Message: "boom"}, false, "HTTP 500"},
		{"deadline exceeded", context.DeadlineExceeded, false, "timed out"},
		{"generic network error", errors.New("connection refused"), false, "network/transport"},
		{"opaque invalid_grant fallback", errors.New("oauth2: cannot fetch token: invalid_grant"), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closed, detail := classifyGCPAPIError(tc.err)
			if closed != tc.wantFailClosed {
				t.Errorf("failClosed = %v, want %v (detail=%q)", closed, tc.wantFailClosed, detail)
			}
			if tc.wantDetailSub != "" && !strings.Contains(detail, tc.wantDetailSub) {
				t.Errorf("detail %q missing %q", detail, tc.wantDetailSub)
			}
		})
	}
}
