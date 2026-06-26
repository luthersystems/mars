#!/usr/bin/env bash
# Verify that the mars image ships a populated terraform provider plugin
# cache for the current arch with the exact provider versions we expect AND
# that the baked /etc/terraformrc makes `terraform init` symlink (not copy)
# the per-arch provider directory into a workdir .terraform/providers/ tree.
# See luthersystems/mars#168 for why the symlink behavior matters.
#
# Fails on:
#   - missing or non-executable provider binary at the expected cache path
#   - filename / version mismatch in /opt/tf-plugin-cache
#   - implausibly small provider binary (build copied a stub)
#   - TF_PLUGIN_CACHE_DIR set (the filesystem_mirror config supersedes it)
#   - TF_CLI_CONFIG_FILE not pointing at /etc/terraformrc
#   - /etc/terraformrc missing or not containing a filesystem_mirror block
#   - terraform init producing "Downloaded" lines instead of mirror hits
#   - the per-arch provider dir in the workdir being a real directory (i.e.
#     copy) instead of a symlink into /opt/tf-plugin-cache
set -euo pipefail

IMAGE="${1:?usage: smoke-tf-cache.sh <image>}"

docker run --rm --entrypoint /bin/bash "${IMAGE}" -c '
set -euo pipefail
shopt -s nullglob

arch=$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)

# (provider-source, version, expected filename in the per-arch dir)
# Keep these in sync with the AWS_PROVIDER_VERSIONS / GOOGLE_PROVIDER_VERSIONS
# build args in the Dockerfile. Enforced at `go test` time by
# TestSmokeExpectMatchesBakedVersions in internal/providercache/.
expect=(
  "hashicorp/aws         6.52.0 terraform-provider-aws_v6.52.0_x5"
  "hashicorp/google      6.10.0 terraform-provider-google_v6.10.0_x5"
  "hashicorp/google-beta 6.10.0 terraform-provider-google-beta_v6.10.0_x5"
)

# Sanity floor on the provider binary size. AWS is ~750 MB, google ~100 MB.
# Set the floor low enough to allow some slack from upstream rebuilds but
# high enough to catch a stub or empty-file regression.
min_bytes=50000000  # 50 MB

fail=0
if command -v flock >/dev/null 2>&1; then
  echo "OK:   flock available for tfenv install serialization"
else
  echo "FAIL: flock is missing"
  fail=1
fi

if [ -x /opt/tfenv/bin/terraform.tfenv-original ] && grep -q "terraform.tfenv-original" /opt/tfenv/bin/terraform; then
  echo "OK:   tfenv terraform shim is flock-guarded"
else
  echo "FAIL: tfenv terraform shim wrapper is missing or incomplete"
  fail=1
fi

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

# Verify pre-installed tfenv versions are present so first-time `tfenv
# install <v>` is a no-op at runtime (saves ~30s/pod that would otherwise
# go to downloading the terraform release tarball + verify + unzip). Keep
# in sync with TFENV_PREINSTALL_VERSIONS in the Dockerfile.
expect_tfenv=(
  "1.9.8"
)
for v in "${expect_tfenv[@]}"; do
  if [ -x "/opt/tfenv/versions/${v}/terraform" ]; then
    echo "OK:   tfenv pre-installed terraform v${v}"
  else
    echo "FAIL: tfenv missing pre-installed terraform v${v} at /opt/tfenv/versions/${v}/terraform"
    fail=1
  fi
done

# End-to-end check: prove the filesystem_mirror config actually makes
# terraform symlink (not copy) the provider into a workdir .terraform/.
# This is the whole point of #168.
#
# Uses 1.9.8 — the version pre-installed in /opt/tfenv/versions/. With the
# pre-install above, `tfenv install 1.9.8` is a no-op rather than the
# ~30s download + verify + unzip dance. Keeps the smoke test path
# consistent with what real consumers (sandbox-infrastructure-template)
# do at runtime via their .terraform-version pin.
have_tf=0
if command -v tfenv >/dev/null 2>&1; then
  # `tfenv install <ver>` writes under /opt/tfenv/versions/ (world-writable).
  # `tfenv use <ver>` would write /opt/tfenv/version (the global selector
  # file) which is NOT world-writable in the mars image, so we set the
  # per-shell override env var instead. The tfenv shim respects it.
  if tfenv install 1.9.8 >/dev/null 2>&1; then
    export TFENV_TERRAFORM_VERSION=1.9.8
    have_tf=1
  fi
elif command -v terraform >/dev/null 2>&1; then
  # Non-tfenv image (unexpected — final image installs tfenv) — try whatever
  # is on PATH.
  have_tf=1
fi

if [ "${have_tf}" -ne 1 ]; then
  echo "FAIL: no terraform available for symlink E2E test (tfenv install/use failed?)"
  fail=1
else
  workdir=$(mktemp -d)
  pushd "${workdir}" >/dev/null
  cat >main.tf <<EOF
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "= 6.52.0" }
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
    # terraform symlinks the per-arch *directory* (not each provider binary
    # individually) from the filesystem_mirror into the workdir. See
    # internal/providercache/package_install.go installFromLocalDir.
    arch_dir="${workdir}/.terraform/providers/registry.terraform.io/hashicorp/aws/6.52.0/linux_${arch}"
    if [ ! -e "${arch_dir}" ]; then
      echo "FAIL: expected provider dir not present at ${arch_dir}"
      find "${workdir}/.terraform/providers" -maxdepth 6 2>&1 || true
      fail=1
    elif [ ! -L "${arch_dir}" ]; then
      echo "FAIL: provider dir ${arch_dir} is a real directory, not a symlink (filesystem_mirror is meant to symlink — Argo tar will duplicate the binary)"
      ls -la "$(dirname "${arch_dir}")" 2>&1
      fail=1
    else
      target=$(readlink "${arch_dir}")
      case "${target}" in
        /opt/tf-plugin-cache/*)
          echo "OK:   terraform init symlinked aws/linux_${arch} into workdir (-> ${target})"
          # Sanity-check the symlink resolves to the actual provider binary.
          if [ ! -x "${arch_dir}/terraform-provider-aws_v6.52.0_x5" ]; then
            echo "FAIL: symlink resolves but expected binary missing or not executable"
            fail=1
          fi ;;
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
