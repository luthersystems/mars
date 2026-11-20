#!/usr/bin/env bash
#
# scripts/test-umbrella-non-root.sh
#
# Runs the umbrella role as a non-root uid and checks that it stages its chart
# on the deploy host (mars#367). Since mars#363 the mars image runs as the
# caller's uid. A non-root uid cannot chown a file to root, so a task that
# sets owner or group to root on the deploy host stops the play. The staged
# files are for helm only, so it is enough that the running user owns them.
#
# Fake kubernetes.core k8s and helm modules, a fake ansible.posix synchronize
# and a fake helm binary stand in for the cluster and the tools, so it needs
# ansible-core and no credentials or network. Run as root, it runs the play as
# the user nobody, because root can chown to root and would hide the bug; then
# nobody must be able to read ansible and this checkout. Linux only (setpriv,
# GNU stat). It stages the chart under a temporary root, not the image's
# /opt/k8s: scripts/smoke-any-uid.sh checks that an arbitrary uid can write
# the image's paths.
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

# fake_module <collection path> <name>: a module that accepts any argument
# and changes nothing.
fake_module() {
  mkdir -p "$TMP/collections/ansible_collections/$1/plugins/modules"
  cat >"$TMP/collections/ansible_collections/$1/plugins/modules/$2.py" <<'PY'
#!/usr/bin/python
from ansible.module_utils.basic import AnsibleModule, _load_params


def main():
    # Accept each argument the task passes, whatever it is.
    spec = {k: dict(type="raw") for k in _load_params()}
    module = AnsibleModule(argument_spec=spec, supports_check_mode=True)
    module.exit_json(changed=False)


if __name__ == "__main__":
    main()
PY
}
fake_module kubernetes/core k8s
fake_module kubernetes/core helm
fake_module ansible/posix synchronize

# The fake helm: "helm dependency update" writes Chart.lock, as helm does.
mkdir -p "$TMP/bin"
cat >"$TMP/bin/helm" <<'SH'
#!/bin/sh
[ "$1 $2" = "dependency update" ] || { echo "fake helm: unexpected: $*" >&2; exit 1; }
: >Chart.lock
SH
chmod +x "$TMP/bin/helm"

cat >"$TMP/inventory.ini" <<'INI'
[all]
localhost ansible_connection=local ansible_python_interpreter=auto_silent
INI

cat >"$TMP/umbrella.yml" <<YML
- hosts: all
  gather_facts: false
  vars:
    # Skip the kubectl and helm setup: the cluster and the tools are fakes.
    kubectl_configured: true
    kubectl_env: {}
    kubectl_asset_root_path: "$TMP/assets"
    mars_local: true
    org_name: test
    luther_env: test
    umbrella_values: {}
  roles:
    - umbrella
YML

# The play runs as the caller, or as nobody when the caller is root. nobody
# has no writable home, so it gets one under $TMP; the caller keeps its own,
# where a pip --user ansible finds its packages.
AS=(env)
if [ "$(id -u)" = 0 ]; then
  chown -R nobody "$TMP"
  AS=(setpriv --reuid=nobody --regid="$(id -gn nobody)" --clear-groups -- env HOME="$TMP")
fi
USER_ID="$("${AS[@]}" id -u)"

if ! "${AS[@]}" ansible-playbook --version >"$TMP/out" 2>&1; then
  cat "$TMP/out"
  echo "ansible-playbook does not run as uid $USER_ID: install ansible where that uid can read it"
  exit 1
fi

rc=0
"${AS[@]}" \
  PATH="$TMP/bin:$PATH" \
  ANSIBLE_ROLES_PATH="$ROLES" \
  ANSIBLE_COLLECTIONS_PATH="$TMP/collections" \
  ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false \
  ANSIBLE_LOCAL_TEMP="$TMP/ansible-tmp" \
  ANSIBLE_REMOTE_TEMP="$TMP/ansible-tmp" \
  ANSIBLE_NOCOLOR=1 \
  ANSIBLE_LOCALHOST_WARNING=false \
  ansible-playbook -i "$TMP/inventory.ini" "$TMP/umbrella.yml" >"$TMP/out" 2>&1 || rc=$?

check "the play runs as a non-root uid" yes "$([ "$USER_ID" != 0 ] && echo yes || echo no)"
check "the umbrella role passes as uid $USER_ID" 0 "$rc"
CHART="$TMP/assets/helmcharts/umbrella"
check "Chart.yaml is staged" yes "$([ -f "$CHART/Chart.yaml" ] && echo yes || echo no)"
check "helm dependency update ran" yes "$([ -f "$CHART/Chart.lock" ] && echo yes || echo no)"
check "the running user owns the chart" "$USER_ID" "$(stat -c %u "$CHART/Chart.yaml" 2>/dev/null || echo none)"

# No role chowns a file it stages on the deploy host to root (mars#367), in
# the YAML form (owner: root) or the inline one (owner=root), by name or uid.
# Left out: bastion_init and journald, which set up a remote host with become.
check "no deploy-host role sets owner or group root" "" \
  "$(grep -rnE "(owner|group)(:[[:space:]]*|=)[\"']?(root|0)[\"']?([[:space:]]|$)" "$ROLES" \
    --include='*.yml' --include='*.yaml' --exclude-dir=bastion_init --exclude-dir=journald || true)"

if [ "$FAILS" -gt 0 ]; then
  echo "--- ansible output ---"
  cat "$TMP/out"
  echo "$FAILS check(s) failed"
  exit 1
fi
echo "all checks passed"
