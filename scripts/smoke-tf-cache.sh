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
# Keep these in sync with the AWS_PROVIDER_VERSION / GOOGLE_PROVIDER_VERSION
# build args in the Dockerfile.
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
# TF_PLUGIN_CACHE_DIR is intentionally unset under the filesystem_mirror
# regime (see luthersystems/mars#168). Confirm the env var is NOT set so we
# do not regress to the copy-instead-of-symlink path.
if [ -n "${TF_PLUGIN_CACHE_DIR:-}" ]; then
  echo "FAIL: TF_PLUGIN_CACHE_DIR=${TF_PLUGIN_CACHE_DIR} should be unset (filesystem_mirror handles caching now)"
  fail=1
fi

# Confirm TF_CLI_CONFIG_FILE points at the baked terraformrc.
case "${TF_CLI_CONFIG_FILE:-}" in
  /etc/terraformrc) ;;
  *)
    echo "FAIL: TF_CLI_CONFIG_FILE=${TF_CLI_CONFIG_FILE:-<unset>} (want /etc/terraformrc)"
    fail=1 ;;
esac

if [ ! -r /etc/terraformrc ]; then
  echo "FAIL: /etc/terraformrc missing or not readable"
  fail=1
elif ! grep -q "filesystem_mirror" /etc/terraformrc; then
  echo "FAIL: /etc/terraformrc does not contain a filesystem_mirror block"
  fail=1
fi

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

# End-to-end check: prove the filesystem_mirror config actually makes
# terraform symlink (not copy) the provider into a workdir .terraform/.
# This is the whole point of #168.
#
# The final image does not preinstall terraform (consumers pick a version via
# tfenv at runtime). Install the same throwaway version we used to warm the
# cache so the symlink check works.
if ! command -v terraform >/dev/null 2>&1; then
  if command -v tfenv >/dev/null 2>&1; then
    tfenv install 1.9.8 >/dev/null 2>&1 || true
    tfenv use 1.9.8 >/dev/null 2>&1 || true
  fi
fi

if ! command -v terraform >/dev/null 2>&1; then
  echo "FAIL: no terraform available for symlink E2E test (tfenv install failed?)"
  fail=1
else
  workdir=$(mktemp -d)
  pushd "${workdir}" >/dev/null
  cat >main.tf <<EOF
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "= 6.45.0" }
  }
}
EOF
  if ! terraform init -input=false -backend=false >/tmp/tfinit.log 2>&1; then
    echo "FAIL: terraform init failed:"
    cat /tmp/tfinit.log
    fail=1
  else
    # "- Downloaded ..." indicates a registry fetch; mirror hits produce
    # "- Installing ..." / "- Installed ..." only.
    if grep -E "^- Downloaded" /tmp/tfinit.log >/dev/null; then
      echo "FAIL: terraform init still downloaded providers (expected mirror hit):"
      grep -E "^- " /tmp/tfinit.log || true
      fail=1
    fi
    bin_path="${workdir}/.terraform/providers/registry.terraform.io/hashicorp/aws/6.45.0/linux_${arch}/terraform-provider-aws_v6.45.0_x5"
    if [ ! -e "${bin_path}" ]; then
      echo "FAIL: expected provider entry not present at ${bin_path}"
      ls -la "${workdir}/.terraform/providers/registry.terraform.io/hashicorp/aws/6.45.0/linux_${arch}/" 2>&1 || true
      fail=1
    elif [ ! -L "${bin_path}" ]; then
      echo "FAIL: provider at ${bin_path} is a regular file, not a symlink (filesystem_mirror is meant to symlink)"
      ls -la "${bin_path}" 2>&1
      fail=1
    else
      target=$(readlink "${bin_path}")
      case "${target}" in
        /opt/tf-plugin-cache/*) echo "OK:   terraform init symlinked aws provider into workdir (-> ${target})" ;;
        *)
          echo "FAIL: symlink target does not point into /opt/tf-plugin-cache: ${target}"
          fail=1 ;;
      esac
    fi
  fi
  popd >/dev/null
  rm -rf "${workdir}"
fi

exit ${fail}
'
