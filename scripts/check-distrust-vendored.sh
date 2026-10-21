#!/usr/bin/env bash
# Check that scripts/distrust-emudhra.sh is still buildenv's
# images/distrust-emudhra.sh at the commit its header names.
#
# Why: the script is vendored (mars builds on ubuntu:24.04, and buildenv has
# no Ubuntu base image to move to), and every Luther image must remove the
# same eMudhra (emSign) roots. A local edit here would quietly make mars
# differ from the other images; change buildenv and copy the file again
# instead, updating the commit in the header.
#
# The copy is the buildenv file with a 5-line provenance comment inserted
# after its shebang; this check removes those lines and compares the rest.
set -euo pipefail

file="$(dirname "$0")/distrust-emudhra.sh"
sha=$(sed -n '4s/^# \([0-9a-f]\{40\}\)\. .*/\1/p' "$file")
if [ -z "$sha" ]; then
	echo "FAIL: $file: line 4 does not name a 40-character buildenv commit" >&2
	exit 1
fi
url="https://raw.githubusercontent.com/luthersystems/buildenv/${sha}/images/distrust-emudhra.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL --retry 3 -o "$work/upstream.sh" "$url"
sed '2,6d' "$file" >"$work/vendored.sh"
if ! diff -u "$work/upstream.sh" "$work/vendored.sh"; then
	echo "FAIL: $file differs from buildenv $sha; change it in buildenv and copy it again" >&2
	exit 1
fi
echo "OK:   $file matches buildenv $sha"
