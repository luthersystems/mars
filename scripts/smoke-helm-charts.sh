#!/usr/bin/env bash
# Render the connectorhub Helm chart with the Helm in the mars image and check
# the container env it produces. values.yaml sets the CH_ENABLE_* defaults
# ("false") and templates/deployment.yaml renders .Values.env. The defaults
# were once nested under dlt.env, where the template never read them, so the
# chart rendered no env at all; this keeps them on the path the template reads.
#
# Usage:
#   smoke-helm-charts.sh <image>   render with the image's helm (CI)
#   smoke-helm-charts.sh --local   render with the helm on PATH
#
# Fails on:
#   - helm template errors
#   - a missing CH_ENABLE_* default, or a wrong value
#   - an env override that drops the other defaults (Helm must merge maps)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHARTS="${ROOT}/ansible-roles/helm_charts/files/helmcharts"

# Runs in the image (or locally) with CHARTS set and helm on PATH.
# shellcheck disable=SC2016 # expanded by the inner shell
CHECKS='
set -uo pipefail

fail=0

render() {
  helm template ch "${CHARTS}/connectorhub" \
    --show-only templates/deployment.yaml "$@"
}

# (description, env name, wanted value, helm args...)
want_env() {
  desc="$1"; name="$2"; want="$3"; shift 3
  if ! out=$(render "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  got=$(printf "%s\n" "${out}" | grep -A1 -e "- name: ${name}\$" |
    sed -n "s/^ *value: //p")
  if [ "${got}" = "\"${want}\"" ]; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: ${name}=${got:-<missing>}, want \"${want}\""
    fail=1
  fi
}

# Chart defaults render. connectorhub defaults each flag to false too
# (cmd/connectorhub/main.go), so these make the documented default explicit.
want_env "default disables the Test API" CH_ENABLE_TEST_API false
want_env "default forces mock MCP in the Test API" CH_ENABLE_TEST_REAL_MCP false
want_env "default disables the Admin API" CH_ENABLE_ADMIN_API false

# A consumer override wins, and Helm merges it with the other defaults
# (ui-infrastructure sets env.CH_ENABLE_TEST_API=true this way).
want_env "env override wins" CH_ENABLE_TEST_API true \
  --set env.CH_ENABLE_TEST_API=true
want_env "env override keeps the other defaults" CH_ENABLE_ADMIN_API false \
  --set env.CH_ENABLE_TEST_API=true

exit ${fail}
'

case "${1:?usage: smoke-helm-charts.sh <image> | --local}" in
--local)
  CHARTS="${CHARTS}" bash -c "${CHECKS}"
  ;;
*)
  docker run --rm -e CHARTS=/charts -v "${CHARTS}:/charts:ro" \
    --entrypoint /bin/bash "$1" -c "${CHECKS}"
  ;;
esac
