#!/usr/bin/env bash
# hack-order-override-watchdog.sh
#
# ────────────────────────────────────────────────────────────────────────
# THIS SCRIPT SHOULD NOT EXIST. GASCITY'S [[orders.overrides]] SHOULD
# BE DURABLE ACROSS AUTOMATIC CONFIG RELOADS.
# ────────────────────────────────────────────────────────────────────────
#
# In gascity 1.1.0, the supervisor's [[orders.overrides]] application
# regresses silently when a NON-USER-INITIATED config reload happens
# (e.g. a pack.toml or agent file is touched, causing the reconciler to
# rebuild config). `gc config show` still displays the override block,
# but the dispatcher reverts to firing the order that was supposed to
# be suppressed.
#
# Observed 2026-05-20:
#   - `[[orders.overrides]] name="mol-dog-jsonl" enabled=false` was
#     applied at 04:05:56Z via `gc supervisor reload`. Verified
#     suppressed (0 fires, 158-second test window).
#   - Automatic config reload at rev `16784a6d58a6` later that day
#     dropped the override application.
#   - `mol-dog-jsonl` fired 55 more times by 12:12Z; 27 ESCALATION:
#     JSONL push failed [HIGH] messages piled up in mayor's inbox.
#   - One `gc supervisor reload` re-suppressed it cleanly.
#
# This script automates the detection + recovery loop. Every 5 minutes
# it parses [[orders.overrides]] from city.toml, picks out the ones
# disabled (`enabled = false`), queries hq.issues for `order:<name>`
# tracking beads created in the last 6 minutes (every order fire writes
# one with created_at = fire time, before the script body runs — so the
# bead exists even for fast-failing orders), and if any are found
# triggers `gc supervisor reload`. Throttled to one reload per 10 min
# so back-to-back reloads can't storm the dispatcher (back-to-back
# reloads cause a 2-5 min dispatch silence on system-pack orders —
# also observed 2026-05-20).
#
# Logs to $GC_CITY/.cache/hack-order-override-watchdog/run-<ts>.log.
#
# Retire when: gascity makes [[orders.overrides]] durable across
# automatic config reloads. Observable as: an override stays in effect
# for at least 24h with no manual reloads, despite normal pack.toml /
# agent file edits triggering automatic reloads in that window.

set -euo pipefail

CITY="${GC_CITY:-$HOME/gc}"
CITY_TOML="$CITY/city.toml"
CACHE="$CITY/.cache/hack-order-override-watchdog"
STATE_FILE="$CACHE/last-reload.epoch"

WINDOW_SECONDS="${WATCHDOG_WINDOW_SECONDS:-360}"   # look back 6 min
RELOAD_COOLDOWN="${WATCHDOG_RELOAD_COOLDOWN:-600}" # min 10 min between reloads

DOLT_HOST="${WATCHDOG_DOLT_HOST:-127.0.0.1}"
DOLT_PORT="${WATCHDOG_DOLT_PORT:-24158}"
DOLT_USER="${WATCHDOG_DOLT_USER:-root}"
DOLT_DB="${WATCHDOG_DOLT_DB:-hq}"

mkdir -p "$CACHE"
RUN_LOG="$CACHE/run-$(date +%s).log"
exec > >(tee "$RUN_LOG") 2>&1

log() { echo "[$(date -Iseconds)] $*"; }

if [[ -f "$CITY/QUOTA_PAUSED" ]]; then
  log "QUOTA_PAUSED — exiting"
  exit 0
fi

if [[ ! -f $CITY_TOML ]]; then
  log "city.toml not found at $CITY_TOML — exiting"
  exit 0
fi

# Parse [[orders.overrides]] blocks where `enabled = false`. Each block
# ends at the next section header (anything starting with `[`) or EOF.
# Anything else inside the block is treated as no-op so unrelated fields
# (scope, rig, formula, etc.) won't perturb parsing.
disabled_overrides() {
  awk '
    function flush() {
      if (in_block && enabled == "false" && name != "") print name
      name = ""; enabled = ""
    }
    /^\[/ {
      flush()
      in_block = ($0 == "[[orders.overrides]]") ? 1 : 0
      next
    }
    in_block && /^name = / {
      n = $0
      sub(/^name = "/, "", n)
      sub(/"$/, "", n)
      name = n
    }
    in_block && /^enabled = false[[:space:]]*$/ { enabled = "false" }
    END { flush() }
  ' "$CITY_TOML"
}

# Returns 0 (success) if the order has fired in the last $WINDOW_SECONDS,
# 1 otherwise. Signal source: `order:<name>` tracking beads in hq, whose
# `created_at` is set at fire time (UTC). Roughly 30x cheaper than
# scanning the dispatcher trace log and independent of trace size.
# On dolt unreachability we log a warning and treat as not-fired — the
# city is in worse shape than this watchdog can fix at that point.
fired_recently() {
  local order="$1"
  local result
  if ! result=$(dolt --host "$DOLT_HOST" --port "$DOLT_PORT" --user "$DOLT_USER" \
    --no-tls --password "" \
    sql -r csv -q "USE \`$DOLT_DB\`;
                                    SELECT COUNT(*) FROM issues
                                    WHERE title = 'order:$order'
                                      AND created_at > UTC_TIMESTAMP() - INTERVAL $WINDOW_SECONDS SECOND;" \
    2>/dev/null | tail -n 1); then
    log "WARN: dolt query for $order failed — treating as not fired"
    return 1
  fi
  if [[ ! $result =~ ^[0-9]+$ ]]; then
    log "WARN: dolt query for $order returned non-numeric '$result' — treating as not fired"
    return 1
  fi
  [[ $result -gt 0 ]]
}

throttle_blocked() {
  if [[ ! -f $STATE_FILE ]]; then
    return 1
  fi
  local last_ts now
  last_ts=$(cat "$STATE_FILE" 2>/dev/null || echo 0)
  now=$(date +%s)
  if [[ $((now - last_ts)) -lt $RELOAD_COOLDOWN ]]; then
    log "throttled — last reload was $((now - last_ts))s ago (< ${RELOAD_COOLDOWN}s)"
    return 0
  fi
  return 1
}

regressions=()
while IFS= read -r order; do
  if fired_recently "$order"; then
    log "REGRESSION: $order is disabled in city.toml but fired in last ${WINDOW_SECONDS}s"
    regressions+=("$order")
  else
    log "$order: quiet (no fires in last ${WINDOW_SECONDS}s)"
  fi
done < <(disabled_overrides)

if [[ ${#regressions[@]} -eq 0 ]]; then
  log "no regressions detected — exiting"
  exit 0
fi

if throttle_blocked; then
  log "regressions present but throttle active — skipping reload"
  exit 0
fi

log "triggering gc supervisor reload to re-assert overrides for: ${regressions[*]}"
if gc supervisor reload >/dev/null 2>&1; then
  date +%s >"$STATE_FILE"
  log "reload succeeded — recorded throttle timestamp"
else
  log "WARN: gc supervisor reload failed"
  exit 1
fi
