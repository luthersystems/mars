#!/usr/bin/env bash
# The per-stage counts of sandbox-infrastructure-template tf/plan-all.sh,
# copied verbatim (the jq filter of its "Extract resource change counts"
# step) from template commit 302656d35775b2e79d664e2969b97ad72e622eb0, so the
# differential test can run it on one plan JSON file: counts.sh <plan.json>.
set -euo pipefail
plan_file="$1"
counts="$(jq '{
  add: [.resource_changes // [] | .[] | select(.change.actions | . != ["no-op"] and . != ["read"]) | select(.change.actions | contains(["create"]))] | length,
  change: [.resource_changes // [] | .[] | select(.change.actions | . != ["no-op"] and . != ["read"]) | select(.change.actions | contains(["update"]))] | length,
  destroy: [.resource_changes // [] | .[] | select(.change.actions | . != ["no-op"] and . != ["read"]) | select(.change.actions | contains(["delete"]))] | length,
  import: [.resource_changes // [] | .[] | select(.change.actions | contains(["import"]))] | length
} | . + {has_changes: ((.add + .change + .destroy + .import) > 0)}' "$plan_file")"
echo "$counts"
