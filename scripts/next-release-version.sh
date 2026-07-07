#!/usr/bin/env bash
# Compute the next release version tag from the latest tag/release.
#
# Used by .github/workflows/mars-release-dispatch.yml so the on-demand release
# can be cut by automation (the official Claude App identity) without a human
# hand-picking a tag. This script is the ONLY sanctioned way to choose that
# version.
#
# BUMP is read from $1 (or the BUMP env var), one of: patch | minor.
#   * patch (default) — safe, never changes the release contract.
#   * minor — mars's historical release cadence (v0.125.0 -> v0.126.0 -> …);
#     allowed because a mars release is normally a dep/feature bump shipped as a
#     minor. MAJOR is never automated — a major implies a contract break and
#     stays human-cut (bump it by hand via the GitHub release UI).
#
# Prints e.g. `v0.128.0` to stdout. Exits non-zero (emits nothing) if the latest
# tag isn't a clean vMAJOR.MINOR.PATCH — don't guess, escalate to a human.
set -euo pipefail

bump="${1:-${BUMP:-patch}}"

# Latest tag: prefer the GitHub release (what :latest tracks) if any releases
# exist; fall back to the highest semver git tag (mars tags without creating a
# GitHub Release object today, so this fallback is the normal path).
latest=""
if command -v gh >/dev/null 2>&1 && latest=$(gh release view --json tagName -q .tagName 2>/dev/null) && [[ -n "${latest}" ]]; then
  : # got it from gh
else
  latest=$(git tag --list 'v[0-9]*' --sort=-v:refname | head -n1)
fi

if [[ -z "${latest}" ]]; then
  echo "error: could not determine the latest release tag" >&2
  exit 1
fi

# Strict vMAJOR.MINOR.PATCH only.
if [[ ! "${latest}" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "error: latest tag '${latest}' is not a clean vMAJOR.MINOR.PATCH; refusing to auto-bump (escalate to a human)" >&2
  exit 2
fi

major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"
patch="${BASH_REMATCH[3]}"

case "${bump}" in
  patch) echo "v${major}.${minor}.$((patch + 1))" ;;
  minor) echo "v${major}.$((minor + 1)).0" ;;
  *)
    echo "error: unknown bump '${bump}' (want: patch | minor; major stays human-cut)" >&2
    exit 3
    ;;
esac
