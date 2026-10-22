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
#   - connectorhub leader election: a wrong checkpoint flag, volume, env,
#     RBAC or PDB, on or off
#   - connectorhub preStop: a missing or wrong hook or grace period, on or
#     off
#   - fabric-peer: http-op or the scrape annotations on the load balancer
#     service, or no ClusterIP operations service
#   - fabric-peer: two services with the same name, or a changed
#     <release>-ops name for a fabric-peer<N>-<org> release
#   - shiroclient: the license state volume, mount or env when off (the
#     default), or a missing or misplaced one when on
#   - fabric-peer, fabric-orderer: a liveness probe that restarts the node
#     on one failed check, a probe without a 5s timeout, or probe timings
#     that values.yaml cannot override (#297)
#   - any chart that runs amazon/aws-cli or docker:*dind: the charts run
#     luthersystems/awscli and luthersystems/dind instead, the same images
#     without the eMudhra (emSign) roots (luthersystems/buildenv)
#   - a chart default awsCLIVersion or dindVersion that differs from the
#     role default (luther_buildenv_images_version, k8s_fabric_peer_dind_version)
#   - fabric-cli: an image other than luthersystems/fabric-tools, or a tag
#     that differs from luther_buildenv_images_version
#   - supergateway, mcp-inspector: an image other than the hardened
#     luthersystems/supergateway-uvx-git (luthersystems/connectorhub)
#   - chaincode: the env the k8s_fabric_chaincode role passes
#     (CHAINCODE_NAME, luthersystems/substrate#808) missing, or dropping
#     the chart's env defaults
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHARTS="${ROOT}/ansible-roles/helm_charts/files/helmcharts"
PEER_CHART="${ROOT}/ansible-roles/k8s_fabric_peer/files/fabric-peer"
ORDERER_CHART="${ROOT}/ansible-roles/k8s_fabric_orderer/files/fabric-orderer"
CC_CHART="${ROOT}/ansible-roles/k8s_fabric_chaincode/files/chaincode"

# Runs in the image (or locally) with CHARTS, PEER_CHART, ORDERER_CHART and
# CC_CHART set
# and helm on PATH.
# shellcheck disable=SC2016 # expanded by the inner shell
CHECKS='
set -uo pipefail

# Never grep -q at the end of a pipeline here: grep -q exits at the first
# match, the writer before it can then die of SIGPIPE, and pipefail turns a
# match into a failure. Send the grep output to /dev/null instead.

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

# (description, yes|no, line, helm args...): does the connectorhub render
# have the line (leading spaces ignored)?
want_render() {
  desc="$1"; want="$2"; str="$3"; shift 3
  if ! out=$(helm template ch "${CHARTS}/connectorhub" "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  if printf "%s\n" "${out}" | sed "s/^ *//" | grep -xF -e "${str}" >/dev/null; then got=yes; else got=no; fi
  if [ "${got}" = "${want}" ]; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: has line \"${str}\": ${got}, want ${want}"
    fail=1
  fi
}

# Leader election (luthersystems/connectorhub#203). Off by default: the
# file checkpoint stays. On: the checkpoint is on the Lease, so no file
# flag (connectorhub refuses it) and no RWO volume, which two replicas on
# different nodes cannot share.
LE="--set leaderElection.enabled=true --set replicaCount=2"
want_render "default keeps the checkpoint file" yes "- --checkpoint-file=/tmp/checkpoint/checkpoint.txt"
want_render "default has no Lease RBAC" no "- apiGroups: [\"coordination.k8s.io\"]"
want_render "default has no PDB" no "kind: PodDisruptionBudget"
want_env "leader election sets CH_HUB_LEADER_ELECTION" CH_HUB_LEADER_ELECTION true ${LE}
want_env "an env override of CH_HUB_LEADER_ELECTION=true renders once" CH_HUB_LEADER_ELECTION true \
  ${LE} --set-string env.CH_HUB_LEADER_ELECTION=true
if helm template ch "${CHARTS}/connectorhub" ${LE} \
  --set-string env.CH_HUB_LEADER_ELECTION=false >/dev/null 2>&1; then
  echo "FAIL: leader election with env.CH_HUB_LEADER_ELECTION=false must not render"
  fail=1
else
  echo "OK:   leader election with env.CH_HUB_LEADER_ELECTION=false fails the render"
fi
want_env "the Lease name defaults to the fullname" CH_HUB_LEADER_ELECTION_LEASE_NAME ch-connectorhub ${LE}
want_env "leaderElection.leaseName sets the Lease name" CH_HUB_LEADER_ELECTION_LEASE_NAME connectorhub-hub \
  ${LE} --set leaderElection.leaseName=connectorhub-hub
want_env "leader election keeps the other env defaults" CH_ENABLE_ADMIN_API false ${LE}
want_render "leader election drops the checkpoint file" no "- --checkpoint-file=/tmp/checkpoint/checkpoint.txt" ${LE}
want_render "leader election drops the checkpoint mount" no "mountPath: /tmp/checkpoint" ${LE}
want_render "leader election drops the checkpoint volume" no "- name: connectorhub-checkpoint" ${LE}
want_render "leader election with 2 replicas starts the new pod first" yes "maxUnavailable: 0" ${LE}
want_render "leader election grants Lease RBAC" yes "- apiGroups: [\"coordination.k8s.io\"]" ${LE}
want_render "leader election with 2 replicas adds a PDB" yes "kind: PodDisruptionBudget" ${LE}
want_render "leader election with 1 replica adds no PDB" no "kind: PodDisruptionBudget" \
  --set leaderElection.enabled=true
want_render "pod label off: no POD_NAME" no "- name: POD_NAME" ${LE}
want_render "pod label on: POD_NAME is set" yes "- name: POD_NAME" \
  ${LE} --set leaderElection.podLabel=true
want_render "pod label off: no RBAC to patch pods" no "verbs: [\"patch\"]" ${LE}
want_render "pod label on: RBAC to patch pods" yes "verbs: [\"patch\"]" \
  ${LE} --set leaderElection.podLabel=true

# preStop hook: SIGTERM waits until the pod is out of the Service
# endpoints, and the grace period covers the sleep and the hub drain.
want_render "preStop on by default" yes "preStop:"
want_render "preStop uses exec sleep by default" yes "command: [\"sleep\", \"15\"]" \
  --kube-version 1.34.0
want_render "preStop mode=auto uses the native sleep on Kubernetes >= 1.30" yes "seconds: 15" \
  --kube-version 1.30.0 --set shutdown.preStop.mode=auto
want_render "preStop mode=auto: native renders no exec" no "command: [\"sleep\", \"15\"]" \
  --kube-version 1.30.0 --set shutdown.preStop.mode=auto
want_render "preStop mode=auto uses exec sleep before Kubernetes 1.30" yes "command: [\"sleep\", \"15\"]" \
  --kube-version 1.29.0 --set shutdown.preStop.mode=auto
want_render "preStop mode=native uses the native sleep" yes "seconds: 15" \
  --set shutdown.preStop.mode=native
want_render "grace period is the sleep plus the drain" yes "terminationGracePeriodSeconds: 45"
want_render "terminationGracePeriodSeconds overrides the grace period" yes "terminationGracePeriodSeconds: 60" \
  --set shutdown.terminationGracePeriodSeconds=60
want_render "preStop off: no hook" no "preStop:" --set shutdown.preStop.enabled=false
want_render "preStop off: explicit grace period of 0 is kept" yes "terminationGracePeriodSeconds: 0" \
  --set shutdown.preStop.enabled=false --set shutdown.terminationGracePeriodSeconds=0
if helm template ch "${CHARTS}/connectorhub" --set shutdown.preStop.enabled=false \
  | grep -q "terminationGracePeriodSeconds"; then
  echo "FAIL: preStop off must render no grace period (Kubernetes default)"
  fail=1
else
  echo "OK:   preStop off: Kubernetes default grace period"
fi
# Each of these must fail the render.
while read -r what args; do
  # shellcheck disable=SC2086
  if helm template ch "${CHARTS}/connectorhub" ${args} >/dev/null 2>&1; then
    echo "FAIL: ${what} must not render"
    fail=1
  else
    echo "OK:   ${what} fails the render"
  fi
done <<'CASES'
grace-period-not-above-sleep --set shutdown.terminationGracePeriodSeconds=15
grace-period-0-with-preStop --set shutdown.terminationGracePeriodSeconds=0
unknown-mode --set shutdown.preStop.mode=foo
sleep-0 --set shutdown.preStop.sleepSeconds=0
sleep-not-a-number --set shutdown.preStop.sleepSeconds=abc
drain-0 --set shutdown.drainSeconds=0
drain-not-a-number --set shutdown.drainSeconds=abc
CASES

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
  if printf "%s\n" "${out}" | grep -xF "${want}" >/dev/null; then
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
if printf "%s\n" "${out}" | grep -x "    service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol: http" >/dev/null &&
  printf "%s\n" "${out}" | grep -Ex "    service.beta.kubernetes.io/aws-load-balancer-healthcheck-port: +\"9443\"" >/dev/null &&
  printf "%s\n" "${out}" | grep -x "    service.beta.kubernetes.io/aws-load-balancer-healthcheck-path: /healthz" >/dev/null; then
  echo "OK:   NLB health check is HTTP /healthz on the operations port"
else
  echo "FAIL: NLB health check is not HTTP /healthz on port 9443"
  fail=1
fi

# Classic ELB (instance targets): the health check goes to a NodePort, and
# 9443 is not a Service port, so it has none. Render no health check
# annotations; the ELB then checks the NodePort of the first Service port.
out=$(peer_svc --set service.useNLB=false)
if printf "%s\n" "${out}" | grep "aws-load-balancer-healthcheck-" >/dev/null; then
  echo "FAIL: classic ELB renders health check annotations for a non-Service port"
  fail=1
else
  echo "OK:   classic ELB renders no health check annotations"
fi

# fabric-peer and fabric-orderer probes (#297): one slow or failed /healthz
# check must not restart the node. The liveness probe allows 3 failures with
# a 5s timeout, the timings come from values.yaml, and an override of one
# field keeps the others (Helm merges maps).
# (chart path, probe kind, helm args...): the top-level fields of the probe,
# one per line as "<field>: <value>". A handler shows as its name alone
# ("httpGet:"); its own fields are dropped.
fabric_probe() {
  chart="$1"; kind="$2"; shift 2
  helm template node "${chart}" --show-only templates/deployment.yaml \
    --set serviceAccount.name=sa --set dlt.organization=org1 \
    --set dlt.domain=example.com "$@" |
    awk -v k="${kind}Probe:" "
      \$1 == k { on=1; next }
      on && /^          [a-zA-Z]/ { on=0 }
      on && /^            [a-zA-Z]/ { sub(/^ +/, \"\"); print }"
}

# (description, chart path, probe kind, wanted fields, helm args...).
# Named apart from the shiroclient want_probe below: CHECKS is one shell, so
# a function of the same name would replace this one.
# Wanted fields are "<field>: <value>" lines joined with ";", in the order
# helm renders them: the default httpGet first, then the values sorted by
# name.
want_fabric_probe() {
  desc="$1"; chart="$2"; kind="$3"; want="$4"; shift 4
  if ! out=$(fabric_probe "${chart}" "${kind}" "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  got=$(printf "%s\n" "${out}" | paste -sd ";" -)
  if [ "${got}" = "${want}" ]; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: got \"${got}\", want \"${want}\""
    fail=1
  fi
}

# (description, chart path, wanted count, helm args...): how many probes of
# any kind the full chart render has. Counting on the whole render, not
# through fabric_probe, so a probe the extractor misses still counts.
want_fabric_probe_count() {
  desc="$1"; chart="$2"; want="$3"; shift 3
  if ! out=$(helm template node "${chart}" --show-only templates/deployment.yaml \
    --set serviceAccount.name=sa --set dlt.organization=org1 \
    --set dlt.domain=example.com "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  got=$(printf "%s\n" "${out}" | grep -cE "^ *(liveness|readiness|startup)Probe:")
  if [ "${got}" = "${want}" ]; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: ${got} probes, want ${want}"
    fail=1
  fi
}

for node in peer orderer; do
  if [ "${node}" = peer ]; then chart="${PEER_CHART}"; else chart="${ORDERER_CHART}"; fi
  want_fabric_probe "${node} liveness tolerates two failed checks" "${chart}" liveness \
    "httpGet:;failureThreshold: 3;periodSeconds: 10;timeoutSeconds: 5"
  want_fabric_probe "${node} readiness has a 5s timeout" "${chart}" readiness \
    "httpGet:;initialDelaySeconds: 5;periodSeconds: 5;timeoutSeconds: 5"
  want_fabric_probe "${node} startup allows 300s, with a 5s timeout" "${chart}" startup \
    "httpGet:;failureThreshold: 30;periodSeconds: 10;timeoutSeconds: 5"
  want_fabric_probe "${node} liveness override wins and keeps the other fields" "${chart}" liveness \
    "httpGet:;failureThreshold: 6;periodSeconds: 10;timeoutSeconds: 5" \
    --set probes.liveness.failureThreshold=6
  want_fabric_probe "${node} liveness handler override replaces httpGet" "${chart}" liveness \
    "failureThreshold: 3;periodSeconds: 10;tcpSocket:;timeoutSeconds: 5" \
    --set probes.liveness.tcpSocket.port=http-op
  want_fabric_probe_count "${node} renders three probes by default" "${chart}" 3
  want_fabric_probe_count "${node} probes.liveness=null turns liveness off" "${chart}" 2 \
    --set probes.liveness=null
  want_fabric_probe_count "${node} renders no probes with startService=false" "${chart}" 0 \
    --set startService=false
done
want_fabric_probe_count "peer renders no probes with startProbes=false" "${PEER_CHART}" 0 \
  --set startProbes=false

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

# (description, yes|no, line, helm args...): does the shiroclient deployment
# render have the line (leading spaces ignored)?
want_sc_render() {
  desc="$1"; want="$2"; str="$3"; shift 3
  if ! out=$(helm template sc "${CHARTS}/shiroclient" \
    --show-only templates/deployment.yaml "$@" 2>&1); then
    echo "FAIL: ${desc}: helm template failed:"
    echo "${out}"
    fail=1
    return
  fi
  # grep -q would exit early and, under pipefail, fail the pipeline on
  # printf SIGPIPE; read all of the input instead.
  if printf "%s\n" "${out}" | sed "s/^ *//" | grep -xF -e "${str}" >/dev/null; then got=yes; else got=no; fi
  if [ "${got}" = "${want}" ]; then
    echo "OK:   ${desc}"
  else
    echo "FAIL: ${desc}: has line \"${str}\": ${got}, want ${want}"
    fail=1
  fi
}

# shiroclient license state directory (luthersystems/mars#282). Off by
# default: no volume, mount or env. On: an emptyDir mounted at mountPath,
# and SHIROCLIENT_LICENSE_STATE_DIR names it.
LS="--set licenseState.enabled=true"
want_sc_render "shiroclient default has no license state volume" no "- name: license-state"
want_sc_render "shiroclient default has no license state env" no "- name: SHIROCLIENT_LICENSE_STATE_DIR"
want_sc_render "shiroclient license state adds the env" yes "- name: SHIROCLIENT_LICENSE_STATE_DIR" ${LS}
want_sc_render "shiroclient license state env names the mount path" yes "value: \"/var/lib/shiroclient/license\"" ${LS}
want_sc_render "shiroclient license state mounts the volume" yes "mountPath: /var/lib/shiroclient/license" ${LS}
# The mount has the same "- name: license-state" line, so check the pod
# volumes list itself (the lines between "volumes:" and "containers:").
sc_volumes=$(helm template sc "${CHARTS}/shiroclient" --show-only templates/deployment.yaml ${LS} 2>&1 |
  awk "/^      volumes:/{p=1;next} /^      containers:/{p=0} p")
if printf "%s\n" "${sc_volumes}" | grep -x "        - name: license-state" >/dev/null; then
  echo "OK:   shiroclient license state adds the volume"
else
  echo "FAIL: shiroclient license state adds the volume: no license-state in volumes:"
  printf "%s\n" "${sc_volumes}"
  fail=1
fi
want_sc_render "shiroclient license state mountPath moves the mount" yes "mountPath: /state" \
  ${LS} --set licenseState.mountPath=/state
want_sc_render "shiroclient license state mountPath moves the env" yes "value: \"/state\"" \
  ${LS} --set licenseState.mountPath=/state
want_sc_render "shiroclient license state sizeLimit renders" yes "sizeLimit: 1Mi" \
  ${LS} --set licenseState.sizeLimit=1Mi

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
    ! printf "%s\n" "${names}" | grep -Evx "[a-z]([-a-z0-9]{0,61}[a-z0-9])?" >/dev/null; then
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

# chaincode (external and CCaaS substrate): the k8s_fabric_chaincode role
# passes an env map with CHAINCODE_NAME, the chaincode definition name, so
# substrate can refuse privileged requests another chaincode forwards
# (luthersystems/substrate#808). Helm must merge that map with the chart
# defaults, or the listen addresses go.
# (description, env name, wanted value)
want_cc_env() {
  desc="$1"; name="$2"; want="$3"
  if ! out=$(helm template substrate-2-300-0-peer0 "${CC_CHART}" \
    --show-only templates/deployment.yaml --set ccid=cc:abc \
    --set env.CHAINCODE_NAME=com_luthersystems_chaincode_substrate01 \
    --set env.CHAINCODE_LOG_LEVEL=info 2>&1); then
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
want_cc_env "chaincode renders CHAINCODE_NAME" CHAINCODE_NAME com_luthersystems_chaincode_substrate01
want_cc_env "chaincode env keeps the listen address default" CHAINCODE_LISTEN_ADDRESS :8080
want_cc_env "chaincode env keeps the metrics address default" CHAINCODE_PROM_LISTEN_ADDR :9600

exit ${fail}
'

# The image checks below read only files, but run after the usage check so a
# bad call fails with the usage line first.
: "${1:?usage: smoke-helm-charts.sh <image> | --local}"

# Upstream images that still trust the eMudhra (emSign) roots. A chart that
# goes back to one would undo luthersystems/buildenv's removal. Search every
# chart, the umbrella charts under helm_charts/files/helmcharts too, for the
# names anywhere (an image: line, or a repository: value). grep exits 1 when
# nothing matches; any other status is an error, not a pass.
set +e
grep -rnE --include='*.yaml' --include='*.tpl' \
  '^[^#]*(amazon/aws-cli|docker(\.io/library/docker)?:[^ "]*dind|repository: *"?docker"? *$)' \
  "${ROOT}/ansible-roles"
rc=$?
set -e
case ${rc} in
0)
  echo "FAIL: a chart runs amazon/aws-cli or docker dind; use luthersystems/awscli or luthersystems/dind"
  exit 1
  ;;
1) echo "OK:   no chart runs amazon/aws-cli or docker dind" ;;
*)
  echo "FAIL: grep for upstream images exited ${rc}"
  exit 1
  ;;
esac

# The YAML value of a top-level key, without quotes or a trailing comment.
yaml_value() {
  sed -n "s/^$1: *//p" "$2" | sed -e 's/ *#.*$//' -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/" | head -n1
}

# Every chart default must equal the role default, so a chart rendered
# without the role's values (helm template, the shiroclient umbrella chart)
# runs the same tag.
fail_tags=0
want_tag() { # <desc> <want> <values.yaml> <key>
  got=$(yaml_value "$4" "$3")
  if [ -z "$2" ] || [ "${got}" != "$2" ]; then
    echo "FAIL: $1: $4 is ${got:-<missing>}, want ${2:-<missing>}"
    fail_tags=1
  else
    echo "OK:   $1: $4 is $2"
  fi
}
buildenv=$(yaml_value luther_buildenv_images_version "${ROOT}/ansible-roles/luther_images/defaults/main.yml")
dind=$(yaml_value k8s_fabric_peer_dind_version "${ROOT}/ansible-roles/k8s_fabric_peer/defaults/main.yml")
for values in \
  "${ROOT}/ansible-roles/k8s_fabric_ca/files/fabric-ca/values.yaml" \
  "${ROOT}/ansible-roles/k8s_fabric_cli/files/fabric-cli/values.yaml" \
  "${ROOT}/ansible-roles/k8s_fabric_orderer/files/fabric-orderer/values.yaml" \
  "${ROOT}/ansible-roles/k8s_fabric_peer/files/fabric-peer/values.yaml" \
  "${ROOT}/ansible-roles/shiroclient_cli/files/shiroclient-cli/values.yaml" \
  "${CHARTS}/shiroclient/values.yaml"; do
  want_tag "${values#"${ROOT}/"}" "${buildenv}" "${values}" awsCLIVersion
done
want_tag "fabric-peer dind" "${dind}" \
  "${ROOT}/ansible-roles/k8s_fabric_peer/files/fabric-peer/values.yaml" dindVersion
# fabric-cli runs luthersystems/fabric-tools, tagged with the buildenv
# release like awscli.
cli_values="${ROOT}/ansible-roles/k8s_fabric_cli/files/fabric-cli/values.yaml"
cli_image=$(sed -n '/^image:/,/^[^ ]/p' "${cli_values}")
cli_repo=$(printf '%s\n' "${cli_image}" | sed -n 's/^  repository: *//p')
cli_tag=$(printf '%s\n' "${cli_image}" | sed -n 's/^  tag: *//p')
if [ "${cli_repo}" != luthersystems/fabric-tools ] || [ "${cli_tag}" != "${buildenv}" ]; then
  echo "FAIL: fabric-cli image is ${cli_repo:-<missing>}:${cli_tag:-<missing>}, want luthersystems/fabric-tools:${buildenv}"
  fail_tags=1
else
  echo "OK:   fabric-cli image is luthersystems/fabric-tools:${buildenv}"
fi
# supergateway and mcp-inspector run the hardened supergateway image, not
# supercorp/supergateway.
for c in supergateway mcp-inspector; do
  sg_repo=$(sed -n '/^image:/,/^[^ ]/p' "${CHARTS}/${c}/values.yaml" | sed -n 's/^  repository: *//p')
  if [ "${sg_repo}" != luthersystems/supergateway-uvx-git ]; then
    echo "FAIL: ${c} image is ${sg_repo:-<missing>}, want luthersystems/supergateway-uvx-git"
    fail_tags=1
  else
    echo "OK:   ${c} image is luthersystems/supergateway-uvx-git"
  fi
done
# A chart that adds an awscli or dind image must be in the list above.
listed=$(grep -rlE --include='*.yaml' 'luthersystems/(awscli|dind):' "${ROOT}/ansible-roles" | wc -l)
if [ "${listed}" -ne 6 ]; then
  echo "FAIL: ${listed} charts run luthersystems/awscli or dind, the tag check covers 6; add the new one"
  fail_tags=1
fi
[ "${fail_tags}" -eq 0 ] || exit 1

case "${1:?usage: smoke-helm-charts.sh <image> | --local}" in
--local)
  CHARTS="${CHARTS}" PEER_CHART="${PEER_CHART}" ORDERER_CHART="${ORDERER_CHART}" \
    CC_CHART="${CC_CHART}" bash -c "${CHECKS}"
  ;;
*)
  docker run --rm -e CHARTS=/charts -v "${CHARTS}:/charts:ro" \
    -e PEER_CHART=/peer-chart -v "${PEER_CHART}:/peer-chart:ro" \
    -e ORDERER_CHART=/orderer-chart -v "${ORDERER_CHART}:/orderer-chart:ro" \
    -e CC_CHART=/cc-chart -v "${CC_CHART}:/cc-chart:ro" \
    --entrypoint /bin/bash "$1" -c "${CHECKS}"
  ;;
esac
