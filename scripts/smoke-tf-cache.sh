#!/usr/bin/env bash
# Verify that the mars image ships a populated terraform provider plugin
# cache for the current arch. Fails if any expected provider binary is
# missing or non-executable.
set -euo pipefail

IMAGE="${1:?usage: smoke-tf-cache.sh <image>}"

docker run --rm --entrypoint /bin/bash "${IMAGE}" -c '
set -euo pipefail
arch=$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)
expect=(
  registry.terraform.io/hashicorp/aws/6.45.0
  registry.terraform.io/hashicorp/google/6.10.0
  registry.terraform.io/hashicorp/google-beta/6.10.0
)
fail=0
for path in "${expect[@]}"; do
  dir="/opt/tf-plugin-cache/${path}/linux_${arch}"
  bin=$(ls "${dir}"/terraform-provider-* 2>/dev/null | head -1 || true)
  if [ -z "${bin}" ] || [ ! -x "${bin}" ]; then
    echo "FAIL: missing or non-executable provider at ${dir}"
    fail=1
  else
    size=$(stat -c %s "${bin}")
    echo "OK: ${path}/linux_${arch} (${size} bytes)"
  fi
done
test -n "${TF_PLUGIN_CACHE_DIR:-}" || { echo "FAIL: TF_PLUGIN_CACHE_DIR not set"; fail=1; }
echo "TF_PLUGIN_CACHE_DIR=${TF_PLUGIN_CACHE_DIR}"
exit ${fail}
'
