#!/usr/bin/env bash
# Prints the release notes for a release of HEAD: one bullet per change on
# main since the previous release tag (a merged PR's title, or a direct
# commit's subject). mars-release-dispatch.yml passes it to the shared release
# workflow as notes_command, which sets LAST_TAG (empty for a first release).
#
# Usage: LAST_TAG=v0.143.0 scripts/release-notes.sh
set -euo pipefail

last_tag="${LAST_TAG:-}"
range=HEAD
if [[ -n "$last_tag" ]]; then
  range="$last_tag..HEAD"
  echo "Changes since $last_tag:"
  echo
fi
git rev-list --first-parent --max-count=200 "$range" | while read -r sha; do
  subject=$(git log -1 --format=%s "$sha")
  if [[ "$subject" =~ ^Merge\ pull\ request\ \#([0-9]+)\ from\  ]]; then
    pr=${BASH_REMATCH[1]}
    # sed quits at the first line, which can end git log with SIGPIPE.
    title=$(git log -1 --format=%b "$sha" | sed -n '/[^[:space:]]/{p;q;}' || true)
    echo "- ${title:-$subject} (#$pr)"
  else
    echo "- $subject (${sha:0:7})"
  fi
done
