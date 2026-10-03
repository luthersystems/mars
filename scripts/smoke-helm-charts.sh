#!/usr/bin/env bash
# Render Helm charts with the Helm in the mars image and check their output.
#
# connectorhub: check the container env it produces. values.yaml sets the CH_ENABLE_* defaults
# ("false") and templates/deployment.yaml renders .Values.env. The defaults
# were once nested under dlt.env, where the template never read them, so the
# chart rendered no env at all; this keeps them on the path the template reads.
#
# fabric-peer: the operations port (metrics, /healthz) must stay off the
# peer's internal load balancer. It has its own ClusterIP service, which
# carries the Prometheus scrape annotations.
#
# Usage:
#   smoke-helm-charts.sh <image>   render with the image's helm (CI)
#   smoke-helm-charts.sh --local   render with the helm on PATH
#
# Fails on:
#   - helm template errors
#   - a missing CH_ENABLE_* default, or a wrong value
#   - an env override that drops the other defaults (Helm must merge maps)
#   - fabric-peer: http-op or the scrape annotations on the load balancer
#     service, or no ClusterIP operations service
#   - fabric-peer: two services with the same name, or a changed
#     <release>-ops name for a fabric-peer<N>-<org> release
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHARTS="${ROOT}/ansible-roles/helm_charts/files/helmcharts"
PEER_CHART="${ROOT}/ansible-roles/k8s_fabric_peer/files/fabric-peer"

# Runs in the image (or locally) with CHARTS and PEER_CHART set and helm on
# PATH.
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

# fabric-peer services of release $1. Each document of
# templates/service.yaml is printed as one line per service:
# "<name> <type> <ports> <scrape>".
peer_services() {
  rel="$1"; shift
  helm template "${rel}" "${PEER_CHART}" \
    --show-only templates/service.yaml \
    --set serviceAccount.name=sa --set dlt.organization=org1 \
    --set dlt.domain=example.com "$@" |
    awk "
      /^---/ { if (n) print n, t, p, s; n=\"\"; t=\"\"; p=\"\"; s=\"no\" }
      /^  name: / && !n { n=\$2 }
      /^  type: / { t=\$2 }
      /^ *name: (grpc|http)-/ { p=p \$2 \",\" }
      /prometheus.io\/scrape: \"true\"/ { s=\"yes\" }
      END { if (n) print n, t, p, s }"
}

# (description, wanted line, helm args...) for the release peer0-org1.
want_service() {
  want_service_as peer0-org1 "$@"
}

# (release name, description, wanted line, helm args...)
want_service_as() {
  rel="$1"; desc="$2"; want="$3"; shift 3
  if ! out=$(peer_services "${rel}" "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  if printf "%s\n" "${out}" | grep -qxF "${want}"; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: want \"${want}\", got:"
    printf "%s\n" "${out}"
    fail=1
  fi
}

want_service "peer load balancer has no operations port" \
  "peer0-org1-fabric-peer LoadBalancer grpc-gossip,grpc-svc, no"
want_service "peer operations port is ClusterIP only, and scraped" \
  "peer0-org1-fabric-peer-ops ClusterIP http-op, yes"
want_service "classic load balancer has no operations port" \
  "peer0-org1-fabric-peer LoadBalancer grpc-gossip,grpc-svc, no" \
  --set service.useNLB=false

# NLB (ip targets): health check HTTP /healthz on the operations port (9443).
# The NLB checks the pod port directly, so 9443 needs no listener. A plain
# TCP check on the TLS peer port (7051) would log a failed TLS handshake for
# each probe.
peer_svc() {
  helm template peer0-org1 "${PEER_CHART}" \
    --show-only templates/service.yaml \
    --set serviceAccount.name=sa --set dlt.organization=org1 \
    --set dlt.domain=example.com "$@" 2>&1
}
out=$(peer_svc)
if printf "%s\n" "${out}" | grep -qx "    service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol: http" &&
  printf "%s\n" "${out}" | grep -Eqx "    service.beta.kubernetes.io/aws-load-balancer-healthcheck-port: +\"9443\"" &&
  printf "%s\n" "${out}" | grep -qx "    service.beta.kubernetes.io/aws-load-balancer-healthcheck-path: /healthz"; then
  echo "OK:   NLB health check is HTTP /healthz on the operations port"
else
  echo "FAIL: NLB health check is not HTTP /healthz on port 9443"
  fail=1
fi

# Classic ELB (instance targets): the health check goes to a NodePort, and
# 9443 is not a Service port, so it has none. Render no health check
# annotations; the ELB then checks the NodePort of the first Service port.
out=$(peer_svc --set service.useNLB=false)
if printf "%s\n" "${out}" | grep -q "aws-load-balancer-healthcheck-"; then
  echo "FAIL: classic ELB renders health check annotations for a non-Service port"
  fail=1
else
  echo "OK:   classic ELB renders no health check annotations"
fi

# shiroclient gateway: the startupProbe renders by default, startupProbe:
# null turns it off, and a custom handler replaces the default httpGet.
startup_probe() {
  helm template sc "${CHARTS}/shiroclient" --show-only templates/deployment.yaml \
    --set runMode=gateway "$@" 2>&1 |
    awk "/^          startupProbe:/{p=1;print;next} p&&/^          [a-zA-Z]/{p=0} p"
}
# (description, wanted handler line count "httpGet exec", helm args...)
want_probe() {
  desc="$1"; want="$2"; shift 2
  out=$(startup_probe "$@")
  got="$(printf "%s\n" "${out}" | grep -c "^            httpGet:") $(printf "%s\n" "${out}" | grep -c "^            exec:")"
  if [ "${got}" = "${want}" ]; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: httpGet/exec count ${got}, want ${want}:"
    printf "%s\n" "${out}"
    fail=1
  fi
}
want_probe "shiroclient startupProbe renders by default" "1 0"
want_probe "shiroclient startupProbe null turns it off" "0 0" \
  --set startupProbe=null
want_probe "shiroclient startupProbe custom handler replaces httpGet" "0 1" \
  --set-json "startupProbe={\"exec\":{\"command\":[\"true\"]},\"periodSeconds\":5}"

# The role names each peer release fabric-peer<N>-<org>. The -ops name of
# such a release must not change, or an upgrade renames the Service.
for rel in fabric-peer0-org1 fabric-peer12-example-organization; do
  want_service_as "${rel}" "release ${rel} keeps its service name" \
    "${rel} LoadBalancer grpc-gossip,grpc-svc, no"
  want_service_as "${rel}" "release ${rel} keeps its -ops service name" \
    "${rel}-ops ClusterIP http-op, yes"
done

# A 63-character fullname keeps its truncated -ops name.
a59=$(printf "a%.0s" $(seq 59))
want_service "63-character fullname keeps its -ops service name" \
  "${a59}-ops ClusterIP http-op, yes" --set fullnameOverride="${a59}aaaa"

# (description, helm args...): the two peer services get distinct names,
# and each name is a DNS-1035 label of at most 63 characters.
want_distinct_services() {
  desc="$1"; shift
  if ! out=$(peer_services peer0-org1 "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  names=$(printf "%s\n" "${out}" | cut -d" " -f1)
  if [ "$(printf "%s\n" "${names}" | grep -c .)" = 2 ] &&
    [ "$(printf "%s\n" "${names}" | sort -u | grep -c .)" = 2 ] &&
    ! printf "%s\n" "${names}" | grep -Evxq "[a-z]([-a-z0-9]{0,61}[a-z0-9])?"; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: want two distinct valid service names, got:"
    printf "%s\n" "${names}"
    fail=1
  fi
}

# A fullname of 62 or 63 characters that ends in -ops truncates to itself
# plus -ops. The -ops service must still get its own name.
a58=$(printf "a%.0s" $(seq 58))
want_distinct_services "63-character fullname gives two distinct service names" \
  --set fullnameOverride="${a59}aaaa"
want_distinct_services "63-character fullname ending in -ops gives two distinct service names" \
  --set fullnameOverride="${a59}-ops"
want_distinct_services "62-character fullname ending in -ops gives two distinct service names" \
  --set fullnameOverride="${a58}-ops"
# peer0-org1-<nameOverride> is a 62-character fullname too.
want_distinct_services "62-character fullname from nameOverride gives two distinct service names" \
  --set nameOverride="${a58:11}-ops"

exit ${fail}
'

case "${1:?usage: smoke-helm-charts.sh <image> | --local}" in
--local)
  CHARTS="${CHARTS}" PEER_CHART="${PEER_CHART}" bash -c "${CHECKS}"
  ;;
*)
  docker run --rm -e CHARTS=/charts -v "${CHARTS}:/charts:ro" \
    -e PEER_CHART=/peer-chart -v "${PEER_CHART}:/peer-chart:ro" \
    --entrypoint /bin/bash "$1" -c "${CHECKS}"
  ;;
esac
