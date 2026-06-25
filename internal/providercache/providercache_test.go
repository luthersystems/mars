// Package providercache holds a CI drift guard for the baked Terraform
// provider cache in the mars image.
//
// Background (reliable#2141): the mars Dockerfile pre-bakes a filesystem mirror
// of the AWS/Google Terraform providers at /opt/tf-plugin-cache so that
// `terraform init` inside an Argo deploy resolves them via symlink instead of a
// ~300MB registry download. The mirror only HITS when the baked version exactly
// matches the version the consumer's .terraform.lock.hcl pins; a miss falls
// back to a registry download that, over a cold customer-account NAT, timed out
// and broke 100% of deploys. The bake had drifted to a single AWS 6.46.0 that
// matched none of the sandbox-infrastructure-template stage locks.
//
// The real source of truth for which versions must be baked lives cross-repo in
// sandbox-infrastructure-template's tf/*/.terraform.lock.hcl files, which mars
// CI does not check out. This guard enforces the next best, same-repo
// invariant: the human-readable "Sources of truth" comment block in the
// Dockerfile (which names each version and the sandbox stage that pins it) must
// stay in lockstep with the actual baked ARG defaults. So whoever bumps the
// bake is forced to keep an accurate, reviewable map that the next operator can
// diff against the sandbox locks — the documentation can't silently rot.
package providercache

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dockerfilePath resolves the repo-root Dockerfile relative to this test file
// (internal/providercache/ -> ../../Dockerfile).
func dockerfilePath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "Dockerfile")
}

var (
	awsArgRe    = regexp.MustCompile(`(?m)^ARG AWS_PROVIDER_VERSIONS="([^"]*)"`)
	googleArgRe = regexp.MustCompile(`(?m)^ARG GOOGLE_PROVIDER_VERSIONS="([^"]*)"`)
	// e.g. `#   AWS    5.48.0  → sandbox tf/{cloud,vm,k8s}-provision`
	srcOfTruthRe = regexp.MustCompile(`(?m)^#\s+(AWS|Google)\s+(\d+\.\d+\.\d+)\b`)
)

func argVersions(t *testing.T, body string, re *regexp.Regexp, name string) []string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("could not find %s ARG in Dockerfile; if it was renamed, update this guard", name)
	}
	return strings.Fields(m[1])
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// TestBakedVersionsMatchSourcesOfTruthComment fails if the Dockerfile's baked
// AWS_PROVIDER_VERSIONS / GOOGLE_PROVIDER_VERSIONS ARGs diverge from the
// "Sources of truth" comment block that documents which sandbox stage pins each
// version. Keeping them aligned guarantees the comment is a trustworthy map for
// the cross-repo check against sandbox-infrastructure-template's lockfiles.
func TestBakedVersionsMatchSourcesOfTruthComment(t *testing.T) {
	raw, err := os.ReadFile(dockerfilePath(t))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	body := string(raw)

	aws := sortedUnique(argVersions(t, body, awsArgRe, "AWS_PROVIDER_VERSIONS"))
	google := sortedUnique(argVersions(t, body, googleArgRe, "GOOGLE_PROVIDER_VERSIONS"))

	if len(aws) == 0 {
		t.Fatal("AWS_PROVIDER_VERSIONS is empty — at least one version must be baked")
	}
	if len(google) == 0 {
		t.Fatal("GOOGLE_PROVIDER_VERSIONS is empty — at least one version must be baked")
	}

	var commentAWS, commentGoogle []string
	for _, m := range srcOfTruthRe.FindAllStringSubmatch(body, -1) {
		switch m[1] {
		case "AWS":
			commentAWS = append(commentAWS, m[2])
		case "Google":
			commentGoogle = append(commentGoogle, m[2])
		}
	}
	commentAWS = sortedUnique(commentAWS)
	commentGoogle = sortedUnique(commentGoogle)

	if strings.Join(aws, " ") != strings.Join(commentAWS, " ") {
		t.Errorf("AWS bake vs 'Sources of truth' comment drift:\n  baked   = %v\n  comment = %v\n"+
			"Update both together (and re-check against sandbox-infrastructure-template tf/*/.terraform.lock.hcl).",
			aws, commentAWS)
	}
	if strings.Join(google, " ") != strings.Join(commentGoogle, " ") {
		t.Errorf("Google bake vs 'Sources of truth' comment drift:\n  baked   = %v\n  comment = %v\n"+
			"Update both together (and re-check against sandbox-infrastructure-template tf/*/.terraform.lock.hcl).",
			google, commentGoogle)
	}
}
