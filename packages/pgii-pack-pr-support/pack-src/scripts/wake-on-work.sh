#!/usr/bin/env bash
# wake-on-work.sh — pgii-pr-support pack
#
# HACK: the gascity 1.x supervisor doesn't always materialize on_demand named
# sessions when their work_query returns hits. This script enumerates the
# on_demand templates we declared in pack.toml and, for each whose work_query
# is non-empty, nudges the supervisor.
#
# Mirrors the legacy hack-wake-on-work.sh; retire when the supervisor bug is
# fixed upstream. See ~/gc/HACKS.md (legacy pack) for the original analysis.

set -euo pipefail

GC_ROOT="${GC_ROOT:-$HOME/gc}"

if [[ -f "$GC_ROOT/QUOTA_PAUSED" ]]; then
  exit 0
fi

if ! command -v gc >/dev/null 2>&1; then
  echo "ERROR: gc CLI not on PATH" >&2
  exit 1
fi

# Templates we manage in this pack. Keep in sync with pack.toml.
TEMPLATES=(pr-self-fixer pr-reviewer pr-triage)

for tmpl in "${TEMPLATES[@]}"; do
  # Skip if a session is already active for this template.
  active=$(env -u BEADS_DIR -u WORKSPACE_ROOT gc session list --json 2>/dev/null |
    jq -r --arg t "$tmpl" 'if type == "array" then \
        [.[] | select(.template == $t and .state == "active")] | length else 0 end' ||
    echo 0)
  if [[ $active -gt 0 ]]; then
    continue
  fi
  # Poke the supervisor; ignore failures (best-effort).
  gc supervisor poke --template "$tmpl" 2>/dev/null || true
done
