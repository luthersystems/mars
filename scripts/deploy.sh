#!/bin/bash
# deploy.sh <vX.Y.Z>
#
# Bumps .mars-version in luthersystems/ui-infrastructure to this release, as
# connectorhub and substrate bump their versions there. The push to
# ui-infrastructure main runs app.yaml on platform-test. The pin is shared with
# prod, which deploys only by a manual dispatch. ui-infrastructure's
# bump_mars_version.sh skips a pre-release tag and never moves the pin back.
set -euo pipefail

VERSION="$1"

# Another release can push to ui-infrastructure main between our clone and our
# push, so a failed clone or a rejected push re-clones and tries again.
for attempt in 1 2 3; do
	rm -rf ui-infrastructure
	if git clone --depth 1 git@github.com:luthersystems/ui-infrastructure.git &&
		(cd ui-infrastructure && ./bump_mars_version.sh "$VERSION"); then
		exit 0
	fi
	[ "$attempt" -lt 3 ] && sleep $((attempt * 5))
done
echo "Could not update ui-infrastructure after 3 attempts" >&2
exit 1
