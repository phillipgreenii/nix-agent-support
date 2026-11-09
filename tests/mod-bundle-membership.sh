#!/usr/bin/env bash
# Rule G1 of docs/superpowers/specs/2026-11-07-mod-bundle-grouping-design.md: every
# plugin that runs code in a session (a hooks/hooks.json with a command hook or a
# `modules` list) appears in exactly one bundle of docs/mod-bundles.json.
#
# usage: mod-bundle-membership.sh BUNDLES_JSON PLUGIN_DIR...
# Prints one FAIL line per violation to stderr; exit status 0 when none, 1 otherwise,
# 99 when no code-running plugin was found (the scan is broken).
set -euo pipefail

bundles="$1"
shift

bad=0
scanned=0
for dir in "$@"; do
  hooks="$dir/hooks/hooks.json"
  [ -f "$hooks" ] || continue
  runs="$(jq -r '([.. | objects | select(.type? == "command")] | length) + ((.modules // []) | length)' "$hooks")"
  [ "$runs" -gt 0 ] || continue
  name="$(jq -r '.name' "$dir/.claude-plugin/plugin.json")"
  scanned=$((scanned + 1))
  count="$(jq -r --arg n "$name" '[.bundles[] | select(index($n) != null)] | length' "$bundles")"
  if [ "$count" -eq 0 ]; then
    echo "FAIL: plugin '$name' runs code in a session but is in no bundle of $bundles" >&2
    bad=$((bad + 1))
  elif [ "$count" -gt 1 ]; then
    echo "FAIL: plugin '$name' is in $count bundles of $bundles, want exactly one" >&2
    bad=$((bad + 1))
  fi
done
echo "scanned $scanned code-running plugin(s)" >&2
[ "$scanned" -gt 0 ] || exit 99
[ "$bad" -eq 0 ]
