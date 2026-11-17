# shellcheck shell=bash
# A stand-in for sandbox-infrastructure-template's shell_utils.sh, for the
# stages differential test: the functions the copied scripts call, without
# their cloud credential and git work. The version functions read
# TEMPLATE_VERSION and PRESETS_VERSION from the environment.
logTemplateVersion() { echo "template_version=${TEMPLATE_VERSION:-unknown}"; }
logPresetsVersion() { echo "presets_version=${PRESETS_VERSION:-unknown}"; }
exportTemplateVersion() { export TEMPLATE_VERSION="${TEMPLATE_VERSION:-}"; logTemplateVersion; }
exportPresetsVersion() { export PRESETS_VERSION="${PRESETS_VERSION:-}"; logPresetsVersion; }
setupCloudEnv() { :; }
cleanupCloudEnv() { :; }
persistInfraRepo() { :; }
gitMergeInfraMain() { :; }
gitSnapshotCommit() { :; }
gitPushInfra() { :; }
