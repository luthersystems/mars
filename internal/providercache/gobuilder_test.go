package providercache

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	// e.g. `FROM golang:1.26.11-bookworm@sha256:<64 hex> AS mars-cli`
	goBuilderRe = regexp.MustCompile(`(?m)^FROM golang:(\S+?)-bookworm(@sha256:[0-9a-f]{64})? AS mars-cli$`)
	toolchainRe = regexp.MustCompile(`(?m)^toolchain go(\S+)$`)
)

// TestGoBuilderMatchesToolchain fails when the Dockerfile's mars-cli stage
// builds with another Go than go.mod's toolchain line names. The golang
// image sets GOTOOLCHAIN=local, so the toolchain line does not apply in
// that stage, while govulncheck and the Go tests read go.mod. A security
// fix made in go.mod alone (GO-2026-6859 was one) would pass every check
// and still ship binaries built with the old Go. The stage must also be
// pinned by digest, so each build of a release uses the same builder.
func TestGoBuilderMatchesToolchain(t *testing.T) {
	dockerfile, err := os.ReadFile(dockerfilePath(t))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(filepath.Dir(dockerfilePath(t)), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	from := goBuilderRe.FindSubmatch(dockerfile)
	if from == nil {
		t.Fatalf("Dockerfile has no `FROM golang:<version>-bookworm AS mars-cli` line")
	}
	toolchain := toolchainRe.FindSubmatch(gomod)
	if toolchain == nil {
		t.Fatalf("go.mod has no toolchain line")
	}
	if string(from[1]) != string(toolchain[1]) {
		t.Errorf("Dockerfile builds with golang:%s, go.mod's toolchain is go%s: move the FROM line (and its digest) to go.mod's version", from[1], toolchain[1])
	}
	if len(from[2]) == 0 {
		t.Errorf("Dockerfile's golang:%s-bookworm is not pinned by digest", from[1])
	}
}
