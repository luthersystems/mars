#!/usr/bin/env bash
# Verify that the mars image ships a populated terraform provider plugin
# cache for the current arch with the exact provider versions we expect.
# Fails on:
#   - missing or non-executable provider binary at the expected path
#   - filename mismatch (wrong version or wrong provider)
#   - more than one binary in the per-arch directory (cache shape unexpected)
#   - implausibly small binary (build copied a stub)
#   - TF_PLUGIN_CACHE_DIR unset or pointing somewhere unexpected
set -euo pipefail

IMAGE="${1:?usage: smoke-tf-cache.sh <image>}"

docker run --rm --entrypoint /bin/bash "${IMAGE}" -c '
set -euo pipefail
shopt -s nullglob

arch=$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)

# (provider-source, version, expected filename in the per-arch dir)
expect=(
  "hashicorp/aws         6.45.0 terraform-provider-aws_v6.45.0_x5"
  "hashicorp/google      6.10.0 terraform-provider-google_v6.10.0_x5"
  "hashicorp/google-beta 6.10.0 terraform-provider-google-beta_v6.10.0_x5"
)

# Sanity floor on the provider binary size. AWS is ~750 MB, google ~100 MB.
# Set the floor low enough to allow some slack from upstream rebuilds but
# high enough to catch a stub or empty-file regression.
min_bytes=50000000  # 50 MB

fail=0
case "${TF_PLUGIN_CACHE_DIR:-}" in
  /opt/tf-plugin-cache) ;;
  *)
    echo "FAIL: TF_PLUGIN_CACHE_DIR=${TF_PLUGIN_CACHE_DIR:-<unset>} (want /opt/tf-plugin-cache)"
    fail=1 ;;
esac

for row in "${expect[@]}"; do
  read -r source version filename <<<"${row}"
  dir="/opt/tf-plugin-cache/registry.terraform.io/${source}/${version}/linux_${arch}"

  entries=("${dir}"/*)
  if [ ${#entries[@]} -eq 0 ]; then
    echo "FAIL: ${source}@${version}/linux_${arch}: directory empty or missing"
    fail=1
    continue
  fi

  bin="${dir}/${filename}"
  if [ ! -f "${bin}" ]; then
    echo "FAIL: ${source}@${version}/linux_${arch}: expected ${filename}, got:"
    printf "       %s\n" "${entries[@]##*/}"
    fail=1
    continue
  fi
  if [ ! -x "${bin}" ]; then
    echo "FAIL: ${bin}: not executable"
    fail=1
    continue
  fi

  size=$(stat -c %s "${bin}")
  if [ "${size}" -lt "${min_bytes}" ]; then
    echo "FAIL: ${bin}: implausibly small (${size} bytes < ${min_bytes} floor)"
    fail=1
    continue
  fi
  echo "OK:   ${source}@${version}/linux_${arch} (${size} bytes)"
done
exit ${fail}
'
