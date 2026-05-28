#!/usr/bin/env bash
# hack-message-forwarder.sh
#
# ────────────────────────────────────────────────────────────────────────
# THIS SCRIPT SHOULD NOT EXIST. `gc mail send <bare>` SHOULD RESOLVE TO
# THE CANONICAL BOUND SESSION, OR BARE-ALIAS MESSAGES SHOULD BE VISIBLE
# IN THE BOUND SESSION'S DEFAULT INBOX.
# ────────────────────────────────────────────────────────────────────────
#
# Companion to HACK 13. The mayor prompt change (HACK 13) makes the
# active `pgii-gastown.mayor` session check `gc mail inbox mayor` on
# startup, catching escalations addressed to the bare alias `mayor`
# (which all currently land at the orphan `gc-pff` session by virtue of
# its registered `alias: mayor`). That covers the *visibility* gap.
#
# This watchdog covers a different gap: bare-alias messages to
# `deacon`, `operator`, or any other agent name without a matching
# orphan have NOWHERE to land. The bead is created with
# `Assignee: <bare>` but no session exists for that name, so it's
# invisible to every session's `gc mail inbox` query and only
# discoverable via `gc bd search`. A future system-pack script or
# user-written formula could escalate to bare `deacon` and produce
# an undeliverable message that disappears.
#
# This script re-assigns open `[message]` beads whose assignee is a
# bare alias to the canonical bound session, so mail addressed to
# `deacon` (or any name in the mapping) eventually lands in
# `pgii-gastown.deacon`'s inbox. Re-assignment only happens after the
# message has been quiet for $STALE_SECONDS to avoid racing live
# senders.
#
# Logs to $GC_CITY/.cache/hack-message-forwarder/run-<ts>.log.
#
# Retire when: one of:
#   1. `gc mail send <bare>` resolves to the canonical bound session
#      directly (upstream fix).
#   2. The pgii-gastown binding is removed and canonical names become
#      bare (no more bare-vs-bound divergence).
#   3. All scripts under our control + all system packs use canonical
#      names exclusively.

set -euo pipefail

CITY="${GC_CITY:-$HOME/gc}"
CACHE="$CITY/.cache/hack-message-forwarder"
STALE_SECONDS="${FORWARDER_STALE_SECONDS:-120}"

mkdir -p "$CACHE"
RUN_LOG="$CACHE/run-$(date +%s).log"
exec > >(tee "$RUN_LOG") 2>&1

log() { echo "[$(date -Iseconds)] $*"; }

if [[ -f "$CITY/QUOTA_PAUSED" ]]; then
  log "QUOTA_PAUSED — exiting"
  exit 0
fi

# Bare-alias → canonical-bound mapping. Keep in sync with the active
# import bindings in city.toml (currently only pgii-gastown applies a
# prefix to mayor / deacon / operator). Edit this when binding changes.
declare -A MAPPING=(
  [mayor]="pgii-gastown.mayor"
  [deacon]="pgii-gastown.deacon"
  [operator]="pgii-gastown.operator"
)

# Cutoff timestamp: messages updated AFTER this are too fresh to forward.
NOW_EPOCH=$(date +%s)
CUTOFF_EPOCH=$((NOW_EPOCH - STALE_SECONDS))

forwarded_total=0
skipped_total=0

for bare in "${!MAPPING[@]}"; do
  canonical="${MAPPING[$bare]}"

  # Fetch all open [message] beads assigned to the bare alias as JSON.
  # The list output uses fields .id, .assignee, .updated_at.
  payload=$(gc bd list --type=message --assignee="$bare" --status=open --json --limit=500 2>/dev/null || echo "[]")

  if [[ -z $payload ]] || [[ $payload == "null" ]]; then
    continue
  fi

  # Filter: only re-assign messages whose updated_at is at least
  # $STALE_SECONDS old, so we don't race fresh sends.
  to_forward=$(printf '%s' "$payload" |
    jq -r --argjson cutoff "$CUTOFF_EPOCH" '
        if type == "array" then
          .[] | select(
            (.assignee // "") == "'"$bare"'"
            and (.updated_at // .created_at // "") != ""
            and ((.updated_at // .created_at) | fromdateiso8601) <= $cutoff
          ) | .id
        else empty end
      ' 2>/dev/null || true)

  if [[ -z $to_forward ]]; then
    log "$bare: 0 stale messages to forward"
    continue
  fi

  while IFS= read -r mid; do
    [[ -z $mid ]] && continue
    if gc bd update "$mid" --assignee="$canonical" >/dev/null 2>&1; then
      log "  forwarded $mid: $bare → $canonical"
      forwarded_total=$((forwarded_total + 1))
    else
      log "  WARN: failed to re-assign $mid (kept at $bare)"
      skipped_total=$((skipped_total + 1))
    fi
  done <<<"$to_forward"
done

log "cycle complete — forwarded=$forwarded_total skipped=$skipped_total"
