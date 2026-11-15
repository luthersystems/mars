#!/usr/bin/env bash
# Verify that the mars image runs as any uid (mars#363), the way a caller
# that runs it as itself starts it: docker run --user "$(id -u):$(id -g)"
# with the checkout mounted at /marsproject.
#
# As a uid other than the image's (10001) and root, with no passwd entry:
#   - mars-entrypoint gives the uid a passwd entry, so ssh runs
#   - HOME is writable, and the helm-diff plugin and the ansible collections
#     installed there are found
#   - mars terraform init, plan and apply work, and ansible-playbook runs
#     a playbook that writes to the checkout and calls helm diff
#   - every file written to the checkout belongs to the caller
set -euo pipefail

IMAGE="${1:?usage: smoke-any-uid.sh <image>}"

# A uid the image has no passwd entry for. The script's own uid when it
# runs as neither root nor 10001 (a CI runner's), else 4321.
RUN_UID=$(id -u)
RUN_GID=$(id -g)
if [ "${RUN_UID}" = 0 ] || [ "${RUN_UID}" = 10001 ]; then
  RUN_UID=4321
  RUN_GID=4321
fi

work=$(mktemp -d)
trap 'docker run --rm --user 0:0 --entrypoint /bin/rm -v "${work}:/w" "${IMAGE}" -rf /w/project >/dev/null 2>&1 || true; rmdir "${work}" 2>/dev/null || true' EXIT
project="${work}/project"
mkdir -p "${project}/vars/default"
echo 1.9.8 >"${project}/.terraform-version"
echo smoke >"${project}/vault_password.txt"
cat >"${project}/main.tf" <<'EOF'
resource "terraform_data" "smoke" {
  input = "any-uid"
}

output "smoke" {
  value = terraform_data.smoke.output
}
EOF
cat >"${project}/smoke.yaml" <<'EOF'
- hosts: localhost
  connection: local
  gather_facts: true
  tasks:
    - name: Write to the checkout
      ansible.builtin.copy:
        content: "ok\n"
        dest: "{{ playbook_dir }}/written-by-ansible"
    - name: Find the helm-diff plugin in HOME
      ansible.builtin.command: helm diff version
      changed_when: false
EOF
if [ "$(id -u)" = 0 ]; then
  chown -R "${RUN_UID}:${RUN_GID}" "${project}"
fi

run() {
  docker run --rm --user "${RUN_UID}:${RUN_GID}" \
    -v "${project}:/marsproject" -w /marsproject "$@"
}

fail=0
check() {
  local name=$1
  shift
  if "$@"; then
    echo "OK:   ${name}"
  else
    echo "FAIL: ${name}"
    fail=1
  fi
}

# mars-entrypoint execs /opt/mars/mars; a stand-in that runs its arguments
# checks the environment mars itself gets.
cat >"${work}/passthru" <<'EOF'
#!/bin/sh
exec "$@"
EOF
chmod a+rx "${work}/passthru"
in_mars_env() {
  run -v "${work}/passthru:/opt/mars/mars:ro" "${IMAGE}" sh -c "$1"
}

check "uid ${RUN_UID} has a passwd entry" in_mars_env 'getent passwd "$(id -u)" >/dev/null'
check "ssh runs as uid ${RUN_UID}" in_mars_env 'ssh -G github.com >/dev/null'
check "HOME is writable" in_mars_env 'touch "$HOME/.smoke" && mkdir -p "$HOME/.kube" "$HOME/.config/gcloud" "$HOME/.terraform.d"'
check "helm finds the helm-diff plugin" in_mars_env 'helm plugin list | grep -q "^diff"'
check "ansible finds the installed collections" in_mars_env 'ansible-galaxy collection list 2>/dev/null | grep -q "^kubernetes.core"'

for step in init plan "apply --approve"; do
  # shellcheck disable=SC2086 # step holds the subcommand and its flag
  check "mars default ${step}" run "${IMAGE}" default ${step}
done
check "terraform state is the caller's" test "$(stat -c %u "${project}/terraform.tfstate" 2>/dev/null)" = "${RUN_UID}"
check "mars default ansible-playbook" run "${IMAGE}" default ansible-playbook smoke.yaml
check "ansible output is the caller's" test "$(stat -c %u "${project}/written-by-ansible" 2>/dev/null)" = "${RUN_UID}"

others=$(find "${project}" ! -uid "${RUN_UID}" -print)
check "no file in the checkout belongs to another uid" test -z "${others}"
if [ -n "${others}" ]; then
  echo "${others}"
fi

exit "${fail}"
