#!/usr/bin/env bash
set -euo pipefail

tfenv_root="${TFENV_ROOT:-/opt/tfenv}"
versions_dir="${TFENV_VERSIONS_DIR:-${tfenv_root}/versions}"
lock_path="${TFENV_INSTALL_LOCK_PATH:-${versions_dir}/.install.lock}"
terraform_original="${tfenv_root}/bin/terraform.tfenv-original"

version="$("${tfenv_root}/bin/tfenv" version-name 2>/dev/null || true)"
if [ -n "${version}" ] && [ "${version}" != "system" ] && [ ! -x "${versions_dir}/${version}/terraform" ]; then
  flock "${lock_path}" "${tfenv_root}/bin/tfenv" install "${version}"
fi

exec "${terraform_original}" "$@"
