#!/usr/bin/env bash
# Stand-in for the template's tf/run-with-creds.sh (its mars wrapper), as in
# the template's tests/test-plan-all.sh: drop the environment argument and
# run terraform. For plan it saves default.tfplan, as ui-core's
# TF_CLI_ARGS_plan -out does.
shift
cmd="$1"
shift
if [[ "$cmd" == "plan" ]]; then
  touch default.tfplan
  exec terraform plan "$@"
fi
exec terraform "$cmd" "$@"
