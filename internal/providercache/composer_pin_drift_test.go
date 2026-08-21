// Cross-repo drift guard: the Terraform provider versions baked into the mars
// image must stay in lockstep with the exact base-provider pins the InsideOut
// composer emits into every customer deploy archive.
//
// Background (flagged by the floating-refs sweep, eks#881): the composer in
// insideout-terraform-presets pins each cloud's base provider to an EXACT
// version (pkg/composer/imported/provider_pins.go, e.g. `= 6.52.0`) so that
// `terraform init` inside an Argo deploy resolves the provider from the
// filesystem mirror this image pre-bakes at /opt/tf-plugin-cache. The mirror
// only HITS on an exact version match; a miss falls back to a ~300MB registry
// download that, over a cold customer-account NAT, times out and breaks the
// deploy (the reliable#2141 incident — see providercache_test.go). The presets
// repo guards its side (its pins can't drift back to an open range), but until
// this test nothing on the mars side caught the reverse: bumping the bake here
// without bumping the presets pin (or merging a presets module bump — e.g. a
// dependabot go-deps PR — without bumping the bake) silently breaks the
// mirror for every composer-emitted customer stack.
//
// Mars already depends on the insideout-terraform-presets Go module (go.mod),
// so this guard reads the pins straight from the module version mars is built
// against — no raw-text cross-repo parsing, and a presets dep bump alone is
// enough to trip it. On a legitimate coordinated bump, update BOTH in one
// review cycle:
//  1. presets: pkg/composer/imported/provider_pins.go (+ its emitter tests)
//  2. mars: Dockerfile AWS_PROVIDER_VERSIONS / GOOGLE_PROVIDER_VERSIONS,
//     the "Sources of truth" comment block, scripts/smoke-tf-cache.sh, and
//     the go.mod pin of insideout-terraform-presets (so this test sees the
//     new upstream value)
package providercache

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	composerpins "github.com/luthersystems/insideout-terraform-presets/pkg/composer/imported"
)

// exactPinRe extracts the version from a composer exact-pin constraint like
// "= 6.52.0".
var exactPinRe = regexp.MustCompile(`^=\s*(\d+\.\d+\.\d+)$`)

// bakedArgForProvider maps a composer required_providers key to the Dockerfile
// ARG whose baked version set must contain its pin. google-beta shares the
// google bake (the tf-providers stage warms both from GOOGLE_PROVIDER_VERSIONS,
// asserted by TestSmokeExpectMatchesBakedVersions).
var bakedArgForProvider = map[string]struct {
	argName string
	re      *regexp.Regexp
}{
	"aws":         {"AWS_PROVIDER_VERSIONS", awsArgRe},
	"google":      {"GOOGLE_PROVIDER_VERSIONS", googleArgRe},
	"google-beta": {"GOOGLE_PROVIDER_VERSIONS", googleArgRe},
}

// TestBakedVersionsMatchComposerPins fails if any base-provider pin emitted by
// the insideout-terraform-presets composer (at the module version pinned in
// mars go.mod) is missing from the provider versions the Dockerfile bakes into
// the image's filesystem mirror. Extra baked versions are allowed (e.g. a
// transition window keeping an old version warm); a composer pin the mirror
// can't serve is not.
func TestBakedVersionsMatchComposerPins(t *testing.T) {
	raw, err := os.ReadFile(dockerfilePath(t))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	body := string(raw)

	baked := map[string][]string{
		"AWS_PROVIDER_VERSIONS":    sortedUnique(argVersions(t, body, awsArgRe, "AWS_PROVIDER_VERSIONS")),
		"GOOGLE_PROVIDER_VERSIONS": sortedUnique(argVersions(t, body, googleArgRe, "GOOGLE_PROVIDER_VERSIONS")),
	}

	pins := composerpins.AllBaseProviderPins()
	if len(pins) == 0 {
		t.Fatal("composer AllBaseProviderPins() returned no pins; if the presets API moved, update this guard")
	}

	for provider, constraint := range pins {
		mapping, ok := bakedArgForProvider[provider]
		if !ok {
			t.Errorf("composer pins provider %q (%s) that this guard does not map to a Dockerfile bake ARG; "+
				"decide whether mars must bake it and teach bakedArgForProvider about it", provider, constraint)
			continue
		}
		m := exactPinRe.FindStringSubmatch(constraint)
		if m == nil {
			t.Errorf("composer pin for %q is %q, not an exact '= X.Y.Z' constraint; "+
				"the mars mirror only serves exact versions — update this guard if the pin format changed deliberately",
				provider, constraint)
			continue
		}
		version := m[1]
		if !slices.Contains(baked[mapping.argName], version) {
			t.Errorf("provider version drift: composer pins %s %s but the Dockerfile bakes %s=%q.\n"+
				"Customer deploys will miss the /opt/tf-plugin-cache mirror and fall back to a registry "+
				"download that times out over cold customer NATs. Bump the Dockerfile bake (ARG, "+
				"'Sources of truth' comment, scripts/smoke-tf-cache.sh) and the presets pin "+
				"(pkg/composer/imported/provider_pins.go) together — see the header comment.",
				provider, version, mapping.argName, fmt.Sprint(baked[mapping.argName]))
		}
	}
}
