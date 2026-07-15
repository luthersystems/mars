#!/usr/bin/env bash
# Verify the mars image ships a working insideout-preflight binary and that
# its exit-code contract holds inside the image, with no cloud credentials.
# The contract (internal/preflight/doc.go) is load-bearing for the
# sandbox-infrastructure-template hook, which treats ONLY exit 1 as fatal:
#   0 = passed OR fail-open, 1 = definitive fail-closed, 2 = usage error.
# Ref: luthersystems/reliable#2243.
#
# Fails on:
#   - binary missing from /usr/local/bin or /opt/bin, or not executable
#   - any deviation from the credential-free exit-code expectations below
set -euo pipefail

IMAGE="${1:?usage: smoke-preflight.sh <image>}"

docker run --rm --entrypoint /bin/bash "${IMAGE}" -c '
set -uo pipefail

fail=0

for p in /usr/local/bin/insideout-preflight /opt/bin/insideout-preflight; do
  if [ -x "${p}" ]; then
    echo "OK:   ${p} present and executable"
  else
    echo "FAIL: ${p} missing or not executable"
    fail=1
  fi
done

# (description, expected exit code, command...)
check() {
  desc="$1"; want="$2"; shift 2
  set +e
  "$@" >/dev/null 2>&1
  got=$?
  set -e
  if [ "${got}" -eq "${want}" ]; then
    echo "OK:   ${desc} (exit ${got})"
  else
    echo "FAIL: ${desc}: exit ${got}, want ${want}"
    fail=1
  fi
}

PF=/usr/local/bin/insideout-preflight

# Usage errors -> 2 (non-fatal to the template hook).
check "bare invocation is a usage error" 2 "${PF}"
check "aws without --actions is a usage error" 2 "${PF}" aws
check "gcp with unreadable credentials file is a usage error" 2 \
  "${PF}" gcp --project-id x --credentials-file /nonexistent --permissions p

# Help -> 0.
check "--help exits zero" 0 "${PF}" --help

# Malformed SA-key JSON -> definitive fail-closed (exit 1), no network.
tmp=$(mktemp)
echo "not json" > "${tmp}"
check "gcp with malformed key JSON fails closed" 1 \
  "${PF}" gcp --project-id x --credentials-file "${tmp}" --permissions p
rm -f "${tmp}"

# No ambient AWS credentials -> credential-resolution error -> fail-open
# (exit 0). AWS_EC2_METADATA_DISABLED avoids a slow IMDS probe on runners.
check "aws with no ambient credentials fails open" 0 \
  env AWS_EC2_METADATA_DISABLED=true AWS_REGION=us-east-1 \
  "${PF}" aws --actions s3:CreateBucket

exit ${fail}
'
