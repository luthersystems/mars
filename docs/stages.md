# Multi-stage plan and apply (`mars-stages.yaml`)

Status: implemented (`plan-all`, `plan-stage`, `apply-stage`); the
template still runs its shell until it moves to these commands. It is the general part of
sandbox-infrastructure-template's `tf/plan-all.sh`, `tf/plan.sh`,
`tf/apply-plan.sh`, `tf/apply-with-outputs.sh` (drift mode) and
`tf/drift-refresh.sh` (sandbox-infrastructure-template#114).

## Why

A project with several Terraform root modules ("stages") that run in order
has, until now, needed its own shell to loop over them, save plans, render
them as JSON, count changes, gate an apply on drift and capture outputs.
mars now does that loop itself. It reads the stages and the file names from
a config file in the project, so it knows nothing about any one project's
layout: the stage names, their directories, which stage a new project must
apply first, and where the reports go are all settings.

What stays with the project: anything that is not Terraform. For the
template that is the git snapshot and push of the infra repo (the
`infra-*` commands, [infra-repo.md](infra-repo.md), which the template
calls), the cloud
credential setup (`setupCloudEnv`, which wraps the mars call), and the
per-stage file staging and backend generation (`tfSetup`, `bootstrap.sh`),
which a stage `setup` hook can run.

## Opt-in

Nothing changes unless you call `plan-all`, `plan-stage` or `apply-stage`
with a `mars-stages.yaml`. Only these three commands read the config;
`drift-check` and `plan-summary` read none, and every other command
(`init`, `plan`, `apply`, `destroy`, `terraform`, `ansible-*`, `packer`,
`alb` and the rest) behaves as before whether or not the file exists. The
stage commands read each prompt's answer through one stdin reader, so
answers piped for several stages all arrive; the other commands keep their
own reader per prompt. `TestOldCommandsMatchMain` (`internal/cli`) holds
the older commands' command lines, environment, output and exit codes to a
golden recorded from main before the stage commands, with no config, a
valid one and an invalid one. Without a config the stage commands fail
(exit 1) and name the file.

## The config file

`mars-stages.yaml` at the project root (`--config <file>` names another).
Every path in it is relative to the directory of the config file (the
project root). Unknown keys are an error, and so are two reports with the
same file: two kinds, two stages, or a stage's report and a global one (a
stage named `outputs` with `files.outputs: "{stage}.json"` is
`outputs.json`, the outputs alias).

```yaml
version: 1

# Directory for the JSON reports (plan JSON, summaries, drift, outputs).
outputs_dir: outputs                 # default: outputs

# Saved plan file name, inside each stage directory.
plan_file: mars.tfplan               # default: mars.tfplan

# Arguments of `terraform init` in each stage.
init_args: ["-input=false", "-reconfigure"]   # the default

# plan-all: go on planning later stages after a stage fails. The run still
# exits 1. false stops at the first failed stage (the summary is still
# written).
continue_on_error: true              # default: true

# plan-all: delete every .terraform/providers directory under the project
# root at the end of the run (terraform init makes them again). Keeps
# provider binaries out of an artifact upload of the project directory.
strip_providers: false               # default: false

# A command run before each stage, in the project root, with MARS_ENV,
# MARS_STAGE and MARS_STAGE_DIR (absolute) set. It may make the stage
# directory. A failure fails the stage. A stage's own setup replaces it;
# a stage's `setup: []` runs none.
setup: ["bash", "tf/stage-setup.sh"] # default: none

# Report file names, inside outputs_dir. {stage} is the stage name.
# An empty value turns that file off.
files:
  plan_json: "tfplan-{stage}.json"   # terraform show -json of the stage's plan
  plan_alias: tfplan.json            # copy of one stage's plan JSON (see below)
  stage_summary: ""                  # full plan-summary report per stage, off by default
  summary: plan-summary.json         # plan-all's totals
  drift: "drift-{stage}.json"        # drift report per stage
  drift_alias: drift.json            # the drift report a later apply reads (see below)
  outputs: "{stage}.json"            # terraform output -json per stage
  outputs_alias: outputs.json        # copy of the last outputs

drift:
  # Fields added to every drift report, from environment variables: the
  # field is the variable's value, or null when it is empty or unset. They
  # go before "resources".
  # A field may not be one of the report's own.
  stamp_env:
    template_version: TEMPLATE_VERSION

stages:
  - name: cloud-provision            # [A-Za-z0-9._-]+, unique
    dir: tf/cloud-provision          # default: the name
    # On a new project (no managed resource in state after the plan),
    # apply this stage's saved plan before planning later stages, so they
    # can read its remote state.
    apply_if_no_state: true
    setup: ["bash", "tf/stage-setup.sh", "cloud-provision"]
  - name: custom-stack-provision
    dir: tf/custom-stack-provision
    # The preferred source of plan_alias. Without one, or when its plan JSON
    # is missing, the last stage with a plan JSON is the source.
    plan_alias_preferred: true
```

Within a stage directory mars works as it does for `mars <env> plan`: it
installs the stage's `.terraform-version` with tfenv, needs `vars/<env>/`,
selects the `<env>` workspace (asking before a switch unless
`--skip-prompt`), and passes `vars/common/*.tfvars` and `vars/<env>/*.tfvars`
to `terraform plan`.

## Commands

### `mars <env> plan-all [--stages a,b] [--continue] [--check-drift] [--config f]`

For each stage, in config order (or in the order of `--stages`):

1. Run the setup hook.
2. `terraform init <init_args>`, `terraform workspace select <env>`.
3. `terraform plan <var files> -out=<plan_file>`.
4. `terraform show -json <plan_file>` into `files.plan_json`.
5. With `apply_if_no_state`: `terraform state list`; when it lists nothing,
   `terraform apply -input=false <plan_file>`, with `TF_CLI_ARGS_plan` and
   `TF_CLI_ARGS_apply` set empty (flags such as `-out` are not valid for
   apply). A failed apply is an error of the run; the stage's counts stay.
6. Count the plan's changes in process (as `plan-summary`): the stage's row
   in the summary, and `files.stage_summary` when set.
7. With `--check-drift`: the drift report in process (as `drift-check`)
   into `files.drift`. It does not change the exit code.

A stage that fails (setup, init, plan, show, state list, or its reports) has
the row `{"error": true}` and none of its reports: no plan JSON, stage
summary, and with `--check-drift` no drift report (without it, the stage's
drift report is not this run's, and the failure leaves it alone).

With `--check-drift`, after the stages, plan-all writes `files.drift_alias`:
one report that combines the `files.drift` reports of every configured
stage that has one, in config order (counts summed, resources in stage
order, `apply_skipped` when any stage's is), so a later apply step reads
one drift report for the whole run. This run's stages' reports are fresh;
with `--continue`, the reports of earlier steps' stages stay and count, so
their drift is not lost. With none it is an empty report
(`drift_detected: false`). When the run fails (a stage failed, a stage was
not planned because `continue_on_error: false` stopped the run, a
new-project apply failed, or `--continue` kept an `{"error": true}` row),
plan-all writes no drift alias and removes an old one, so no alias reads
as clean for a run that did not finish.

Before the stages, plan-all deletes stale reports, only by the names the
config gives (never by a wildcard): `files.plan_json` and
`files.stage_summary` and `files.drift` of every configured stage,
`files.plan_alias`, `files.summary`, and with `--check-drift`
`files.drift_alias`. Cleanup knows only the stages in the config: the
reports of a stage that was renamed or removed from the config stay, so
delete them by hand. With `--continue` (the template's
`PLAN_ALL_CONTINUE=true`) it deletes only this run's stages' plan JSON,
stage summaries and drift reports and the alias, and keeps the rows of the
summary's other stages: this run continues a plan-all that an earlier step
started. A kept `{"error": true}` row still makes the run exit 1, as in
plan-all.sh.

`files.summary` (field for field the template's `plan-summary.json`):

```json
{
  "stages": {
    "cloud-provision": {"add": 1, "change": 0, "destroy": 0, "import": 0, "has_changes": true},
    "custom-stack-provision": {"error": true}
  },
  "total": {"add": 1, "change": 0, "destroy": 0, "import": 0},
  "has_changes": true
}
```

Kept rows come first, sorted by name, then this run's stages in run order.
Totals skip error rows. `has_changes` is true when any row has changes. The
counts are `plan-summary`'s, so a row's `import` also counts
`change.importing` and its `has_changes` also counts forgets, where the
template's jq counted neither.

Then `files.plan_alias` is copied from the preferred stage's plan JSON, or
the last stage (kept ones first) with a plan JSON. The summary JSON is also
printed on stdout.

Exit codes: 0 every stage planned (changes are in the summary, not the exit
code); 1 a stage failed, an apply of a new project failed, or the config is
bad.

### `mars <env> plan-stage <stage> [--refresh-only] [--check-drift] [--strict] [--ignore-drift] [--plan-file f]`

One stage of plan-all (steps 1 to 4, 6, without the new-project apply), plus
`files.plan_alias`. It is the general part of `plan.sh` and, with
`--refresh-only --check-drift --strict --plan-file refresh.tfplan`, of
`drift-refresh.sh`. With `--check-drift` it writes `files.drift` and
`files.drift_alias` (the same document) and exits as `drift-check` does.

It first deletes the stage's own stale reports (plan JSON, stage summary,
and with `--check-drift` its drift report; without it the drift report is
neither written nor removed). When the plan itself fails, the aliases stay as they were.
When a later step fails (show, counts, drift report), it deletes the
stage's reports and the aliases this run would have written, so no alias
points at a half-written run.

Exit codes: 0 planned (no drift, or drift that does not count); 2 with
`--check-drift`: actionable drift, or any drift with `--strict`, unless
`--ignore-drift`; 1 error.

### `mars <env> apply-stage <stage> [--plan f] [--check-drift] [--ignore-drift] [--outputs] [--clean-providers]`

1. Run the setup hook. With `--plan`, the file (in the stage directory) must
   exist.
2. With `--clean-providers`, delete the stage's `.terraform/providers`
   (`apply-plan.sh` does this before its init), through an `os.Root` of the
   project: a `.terraform` that links out of the project is an error.
3. `terraform init <init_args>`, `terraform workspace select <env>`.
4. Without `--plan`: `terraform plan <var files> -out=<plan_file>` and apply
   that.
5. With `--check-drift`: the drift report into `files.drift` and
   `files.drift_alias`. When `apply_skipped` is true (actionable drift and no
   `--ignore-drift`), stop here: no apply, exit 0, as the template's apply
   wrappers do since template#108.
6. `terraform apply -input=false <plan>`.
7. With `--outputs`: `terraform output -json` into `files.outputs` and
   `files.outputs_alias`.
8. Without `--check-drift`, when `files.drift_alias` does not exist: an empty
   drift report there (`drift_detected: false`, `resources: []`), as the
   template's stub.

`apply-plan.sh <stage> --plan-file <id>` is `apply-stage <stage> --plan
<id>.tfplan --outputs --clean-providers`, and `apply-with-outputs.sh <stage>
--check-drift` is `apply-stage <stage> --check-drift --outputs`.

Exit codes: 0 applied, or the apply skipped on drift (`apply_skipped` in the
report); 1 error.

## Tests

`internal/terraform/stages_test.go` runs the commands on a fake runner.
`internal/terraform/stages_diff_test.go` runs the template's scripts
(copied into `testdata/stages/template`) and the mars commands on the same
fake project and fake terraform, and compares exit codes, applied stages
and every JSON file field for field.

## Differences from the template's shell

- The plan is always saved (`-out=<plan_file>`), so a stage always has plan
  JSON. The template saved one only when ui-core's `TF_CLI_ARGS_plan` had
  `-out`. mars's `-out` comes after `TF_CLI_ARGS_plan` on terraform's
  command line, so it wins; a caller that needs a given name passes
  `--plan-file` (plan-stage) or sets `plan_file`.
- The new-project apply applies the saved plan instead of planning again.
- A failed `terraform state list` is an error of the stage. The template read
  it as an empty state and applied.
- `apply-stage` plans with the var files, as `mars plan` does; the template's
  drift mode ran a bare `terraform plan`.
- The drift report has `actionable_count` too, and per-stage counts follow
  `plan-summary` (see above).
- A drift check writes its reports when there is no drift too
  (`drift_detected: false`); the template's drift-check.sh wrote none then.
- Not ported: the git steps, cloud credentials, version log lines and the
  template's file staging. A `setup` hook runs as a child process, so an
  `export` in it does not reach mars; export such variables before calling
  mars.
