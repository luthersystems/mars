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
	// expect-array rows in scripts/smoke-tf-cache.sh, e.g.
	//   "hashicorp/google-beta 7.16.0 terraform-provider-google-beta_v7.16.0_x5"
	smokeExpectRe = regexp.MustCompile(`"hashicorp/([a-z-]+)\s+(\d+\.\d+\.\d+)\s`)
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

// smokeScriptPath resolves scripts/smoke-tf-cache.sh relative to this test file
// (internal/providercache/ -> ../../scripts/smoke-tf-cache.sh).
func smokeScriptPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "scripts", "smoke-tf-cache.sh")
}

// TestSmokeExpectMatchesBakedVersions fails if the provider versions the CI
// smoke test (scripts/smoke-tf-cache.sh) asserts are present in the image cache
// drift from the versions the Dockerfile actually bakes. That smoke test runs
// only after a full (~7 min) multi-arch image build, so without this same-repo
// guard a version bump that updates the Dockerfile ARGs but not the smoke
// script's expect array fails late and expensively — exactly what happened when
// the bake moved to AWS 6.46.0 / Google 7.16.0 but the script still expected
// google 6.10.0. google-beta tracks the google version.
func TestSmokeExpectMatchesBakedVersions(t *testing.T) {
	dockerfile, err := os.ReadFile(dockerfilePath(t))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	bakedAWS := sortedUnique(argVersions(t, string(dockerfile), awsArgRe, "AWS_PROVIDER_VERSIONS"))
	bakedGoogle := sortedUnique(argVersions(t, string(dockerfile), googleArgRe, "GOOGLE_PROVIDER_VERSIONS"))

	smoke, err := os.ReadFile(smokeScriptPath(t))
	if err != nil {
		t.Fatalf("read smoke script: %v", err)
	}

	var smokeAWS, smokeGoogle, smokeGoogleBeta []string
	for _, m := range smokeExpectRe.FindAllStringSubmatch(string(smoke), -1) {
		switch m[1] {
		case "aws":
			smokeAWS = append(smokeAWS, m[2])
		case "google":
			smokeGoogle = append(smokeGoogle, m[2])
		case "google-beta":
			smokeGoogleBeta = append(smokeGoogleBeta, m[2])
		}
	}
	smokeAWS = sortedUnique(smokeAWS)
	smokeGoogle = sortedUnique(smokeGoogle)
	smokeGoogleBeta = sortedUnique(smokeGoogleBeta)

	if len(smokeAWS) == 0 || len(smokeGoogle) == 0 || len(smokeGoogleBeta) == 0 {
		t.Fatalf("could not parse smoke-tf-cache.sh expect array "+
			"(aws=%v google=%v google-beta=%v); if its format changed, update smokeExpectRe",
			smokeAWS, smokeGoogle, smokeGoogleBeta)
	}

	if strings.Join(smokeAWS, " ") != strings.Join(bakedAWS, " ") {
		t.Errorf("smoke-tf-cache.sh AWS versions drift from the Dockerfile bake:\n  smoke = %v\n  baked = %v", smokeAWS, bakedAWS)
	}
	if strings.Join(smokeGoogle, " ") != strings.Join(bakedGoogle, " ") {
		t.Errorf("smoke-tf-cache.sh google versions drift from the Dockerfile bake:\n  smoke = %v\n  baked = %v", smokeGoogle, bakedGoogle)
	}
	if strings.Join(smokeGoogleBeta, " ") != strings.Join(bakedGoogle, " ") {
		t.Errorf("smoke-tf-cache.sh google-beta versions drift from the Dockerfile google bake:\n  smoke = %v\n  baked = %v", smokeGoogleBeta, bakedGoogle)
	}
}
