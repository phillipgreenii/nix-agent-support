#!/usr/bin/env bash
# pr-watcher.sh — pgii-pr-support pack
#
# One cycle: invoke `pg-pr sync` to ingest GitHub state into beads, then
# `pg-pr changes --since <ts>` to see what changed and (optionally) dispatch
# the gascity agents.
#
# This is the pg-pr-driven replacement for the legacy
# ~/gc/assets/imports/zr/scripts/pr-watcher.sh, which did all
# the GraphQL + bead upsert work itself. Now that lives inside `pg-pr sync`
# in agent-support.
#
# Sentinel gates: ~/gc/QUOTA_PAUSED.
# Logs to ~/gc/.cache/pgii-pr-support/run-<ts>.log.

set -euo pipefail

GC_ROOT="${GC_ROOT:-$HOME/gc}"
CACHE="${GC_ROOT}/.cache/pgii-pr-support"
STATE="${CACHE}/state"

mkdir -p "$CACHE" "$STATE"
RUN_LOG="$CACHE/run-$(date +%s).log"
exec > >(tee "$RUN_LOG") 2>&1

log() { echo "[$(date -Iseconds)] $*"; }

# --- sentinel gates ---
if [[ -f "$GC_ROOT/QUOTA_PAUSED" ]]; then
  log "QUOTA_PAUSED — exiting"
  exit 0
fi

# --- locate pg-pr ---
if ! command -v pg-pr >/dev/null 2>&1; then
  log "ERROR: pg-pr not on PATH; install phillipgreenii-nix-agent-support modules"
  exit 1
fi

# --- compute since timestamp from last successful tick ---
LAST_TICK_FILE="$STATE/last-tick"
if [[ -f $LAST_TICK_FILE ]]; then
  since=$(<"$LAST_TICK_FILE")
else
  # First run: look back one hour. pg-pr sync is idempotent so any duplicates
  # are no-ops.
  since=$(date -u -v-1H -Iseconds 2>/dev/null || date -u -d '1 hour ago' -Iseconds)
fi
this_tick=$(date -u -Iseconds)

log "tick start since=$since now=$this_tick"

# --- sync ---
log "running: pg-pr sync"
if ! pg-pr sync; then
  log "WARN: pg-pr sync returned non-zero; continuing to changes anyway"
fi

# --- report changes since last successful tick ---
log "running: pg-pr changes --since $since"
changes_file="$CACHE/changes-$(date +%s).json"
if pg-pr changes --since "$since" --json >"$changes_file" 2>>"$RUN_LOG"; then
  # Count changed beads. If pg-pr changes --json returns a list, length is
  # straightforward. If it returns an object with a key like "changes", adapt.
  count=$(jq 'if type == "array" then length elif (has("changes")) then (.changes | length) else 0 end' "$changes_file" 2>/dev/null || echo 0)
  log "  $count changed bead(s)"
else
  log "WARN: pg-pr changes failed; leaving last-tick unchanged so next cycle retries"
  rm -f "$changes_file"
  exit 0
fi

# --- advance last-tick on success ---
printf '%s\n' "$this_tick" >"$LAST_TICK_FILE"
log "tick complete; last-tick advanced to $this_tick"

# Note: gascity agents (pr-self-fixer, pr-reviewer, pr-triage) discover their
# work via their own work_query bd queries. We don't dispatch them from here;
# the supervisor handles materialization (with help from wake-on-work.sh
# during the HACK 1 workaround window).
