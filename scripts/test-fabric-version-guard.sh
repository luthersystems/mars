#!/usr/bin/env bash
#
# scripts/test-fabric-version-guard.sh
#
# Runs the k8s_fabric_version_guard role (mars#348) against fake Fabric
# deployments and checks that it refuses a version change outside an upgrade
# run and admits the rest. It also checks that k8s_fabric_orderer and
# k8s_fabric_peer run the guard before they change anything. A fake
# kubernetes.core.k8s_info module stands in for the cluster, so it needs
# ansible-core and no credentials or network.
# CI: the fabric3-upgrade job of .github/workflows/mars-ci.yml.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROLES="$ROOT/ansible-roles"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAILS=0
check() { # check <name> <want> <got>
  if [ "$2" = "$3" ]; then printf 'ok   %s\n' "$1"; else
    printf 'FAIL %s (got "%s", want "%s")\n' "$1" "$3" "$2"
    FAILS=$((FAILS + 1))
  fi
}

# The fake k8s_info: returns the deployments of the namespace from the JSON
# map in $FAKE_K8S_DATA ({"<namespace>": [<deployment>...]}).
MOD="$TMP/collections/ansible_collections/kubernetes/core/plugins/modules"
mkdir -p "$MOD"
cat >"$MOD/k8s_info.py" <<'PY'
#!/usr/bin/python
import json
import os

from ansible.module_utils.basic import AnsibleModule


def main():
    module = AnsibleModule(
        argument_spec=dict(
            api_version=dict(type="str"),
            kind=dict(type="str"),
            namespace=dict(type="str"),
        ),
        supports_check_mode=True,
    )
    with open(os.environ["FAKE_K8S_DATA"]) as f:
        data = json.load(f)
    module.exit_json(changed=False, resources=data.get(module.params["namespace"], []))


if __name__ == "__main__":
    main()
PY

cat >"$TMP/inventory.ini" <<'INI'
[all]
localhost ansible_connection=local ansible_python_interpreter=auto_silent
INI

# The guard as each role calls it.
cat >"$TMP/guard.yml" <<'YML'
- hosts: all
  gather_facts: false
  vars:
    kubectl_env: {}
  tasks:
    - name: Orderer guard
      include_role:
        name: k8s_fabric_version_guard
      vars:
        k8s_fabric_version_guard_namespace: fabric-orderer
        k8s_fabric_version_guard_container: fabric-orderer
        k8s_fabric_version_guard_version: "{{ orderer_version | default(fabric_version) }}"
    - name: Peer guard
      include_role:
        name: k8s_fabric_version_guard
      vars:
        k8s_fabric_version_guard_namespace: fabric-org1
        k8s_fabric_version_guard_container: fabric-peer
        k8s_fabric_version_guard_version: "{{ peer_version | default(fabric_version) }}"
YML

# deployment <namespace> <name> <container=image>...: one Deployment as JSON.
deployment() {
  local ns="$1" name="$2" containers="" c
  shift 2
  for c in "$@"; do
    containers+="${containers:+,}{\"name\":\"${c%%=*}\",\"image\":\"${c#*=}\"}"
  done
  printf '{"metadata":{"namespace":"%s","name":"%s"},"spec":{"template":{"spec":{"containers":[%s]}}}}' \
    "$ns" "$name" "$containers"
}

# cluster <orderer image> <peer image>: writes the fake cluster. An empty
# image leaves that deployment out.
cluster() {
  local orderer="" peer=""
  [ -z "$1" ] || orderer="$(deployment fabric-orderer fabric-orderer0 "fabric-orderer=$1" awscli=luthersystems/awscli:2.0.0)"
  [ -z "$2" ] || peer="$(deployment fabric-org1 fabric-peer0-org1 "fabric-peer=$2" dind=luthersystems/dind:29.0.0 archiver=luthersystems/fabric-snapshot-archiver:v0.0.9)"
  printf '{"fabric-orderer":[%s],"fabric-org1":[%s]}' "$orderer" "$peer" >"$TMP/k8s.json"
}

# run <extra var or -flag>...: prints "pass" or "fail", and keeps the output
# in $TMP/out. An argument that starts with - goes to ansible-playbook as is.
run() {
  local rc=0 args=() v
  for v in "$@"; do
    case "$v" in -*) args+=("$v") ;; *) args+=(-e "$v") ;; esac
  done
  ANSIBLE_ROLES_PATH="$ROLES" \
    ANSIBLE_COLLECTIONS_PATH="$TMP/collections" \
    ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false \
    ANSIBLE_NOCOLOR=1 \
    ANSIBLE_LOCALHOST_WARNING=false \
    FAKE_K8S_DATA="$TMP/k8s.json" \
    ansible-playbook -i "$TMP/inventory.ini" "$TMP/guard.yml" \
    -e fabric_version=3.1.5 "${args[@]}" >"$TMP/out" 2>&1 || rc=$?
  if [ "$rc" = 0 ]; then echo pass; else echo fail; fi
}

has() { grep -q -- "$1" "$TMP/out" && echo yes || echo no; }

cluster hyperledger/fabric-orderer:3.1.5 hyperledger/fabric-peer:3.1.5
check "same version passes" pass "$(run)"
check "the check ran" yes "$(has 'Running fabric-peer nodes: hyperledger/fabric-peer:3.1.5')"
check "check mode runs the check" pass "$(run --check)"
check "check mode lists the nodes" yes "$(has 'Running fabric-orderer nodes: hyperledger/fabric-orderer:3.1.5')"

cluster hyperledger/fabric-orderer:3.1.5 hyperledger/fabric-peer:2.5.12
check "an older peer fails" fail "$(run)"
check "the message names the node" yes "$(has 'fabric-org1/fabric-peer0-org1 runs hyperledger/fabric-peer:2.5.12')"

cluster hyperledger/fabric-orderer:2.5.12 hyperledger/fabric-peer:2.5.12
check "an older orderer fails" fail "$(run)"
check "the orderer fails first" yes "$(has 'fabric-orderer/fabric-orderer0 runs hyperledger/fabric-orderer:2.5.12')"
check "a downgrade fails" fail "$(run fabric_version=2.2.15)"
check "check mode fails too" fail "$(run --check)"
check "check mode fails at the guard" yes "$(has 'That is a Fabric upgrade')"
check "an upgrade run skips the check" pass "$(run is_fabric_upgrade=true)"

cluster "luthersystems/fabric-orderer:v3.1.5@sha256:0123" luthersystems/fabric-peer:3.1.5
check "another repository, a v prefix and a digest pass" pass "$(run)"

cluster hyperledger/fabric-orderer:3.1.5 hyperledger/fabric-peer:3.1.5
check "a per-node version is compared" fail "$(run peer_version=3.1.6)"
check "a v prefix on the version passes" pass "$(run fabric_version=v3.1.5)"

cluster "" ""
check "a network with no nodes passes" pass "$(run)"

# The roles run the guard before their first change (the namespace task).
for role in k8s_fabric_orderer k8s_fabric_peer; do
  f="$ROLES/$role/tasks/main.yml"
  guard="$(grep -n 'name: k8s_fabric_version_guard' "$f" | head -1 | cut -d: -f1)"
  first="$(grep -n 'name: K8s namespace exists' "$f" | head -1 | cut -d: -f1)"
  check "$role runs the guard before any change" yes "$([ -n "$guard" ] && [ -n "$first" ] && [ "$guard" -lt "$first" ] && echo yes || echo no)"
done

if [ "$FAILS" -gt 0 ]; then
  echo "$FAILS check(s) failed"
  exit 1
fi
echo "all checks passed"
