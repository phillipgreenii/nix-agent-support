#!/bin/sh
# wake-watchdog.sh — periodic poll that spawns on_demand sessions
# when their mailboxes have unread mail and no live session is
# processing.
#
# This shim exists because gc 1.1.0's on_demand reconciler does not
# reliably materialize sessions from mail signals. The original
# design relied on the supervisor noticing a mail arrival for an
# on_demand template and spawning the session; that path is broken
# and on_demand sessions stay reserved-unmaterialized while mail
# piles up.
#
# Without this, mol-triage-poll would also need to know how to spawn
# sessions itself. Per the spec, the order ONLY notifies (sends mail);
# this watchdog handles the spawn-if-needed responsibility. That
# split keeps each piece small: orders signal, this watchdog adapts.
#
# Templates we watch are listed below. Add new on_demand templates
# here as they're introduced.
#
# Retire when: gc upstream materializes on_demand sessions from mail
# without help. Observable as: a fresh wake of any listed template
# happens within ~60s of mail arrival, with no entry of this script
# in the order history needed.

set -u

GC_CITY="${GC_CITY:-$HOME/gc}"

if [ -f "$GC_CITY/QUOTA_PAUSED" ]; then
  exit 0
fi

# All on_demand templates we want to keep awake when mail arrives.
# pgii-foremen agents + pgii-pr-support agents.
TEMPLATES="triager city-foreman zr-foreman personal-foreman city-worker pr-self-fixer pr-reviewer pr-triage"

spawned=0
skipped_no_mail=0
skipped_live=0

for tmpl in $TEMPLATES; do
  # gc mail count <tmpl> prints e.g. "16 total, 16 unread for pgii-foremen.triager".
  # We just need the unread count; default to 0 on any failure.
  unread=$(gc mail count "$tmpl" 2>/dev/null |
    grep -oE '[0-9]+ unread' |
    awk '{print $1}')
  unread="${unread:-0}"

  if [ "$unread" -le 0 ]; then
    skipped_no_mail=$((skipped_no_mail + 1))
    continue
  fi

  # Check for a live (active or creating) session whose template name
  # ends in ".$tmpl" (bound) or equals $tmpl (bare). awk col 2 is the
  # full bound template name, col 3 is the state.
  live=$(gc session list 2>/dev/null |
    awk -v t="$tmpl" '
            NR > 1 && ($2 ~ ("\\." t "$") || $2 == t) \
                  && ($3 == "active" || $3 == "creating") {
                c++
            }
            END { print c + 0 }
          ')

  if [ "${live:-0}" -gt 0 ]; then
    skipped_live=$((skipped_live + 1))
    continue
  fi

  # Unread mail and no live session — spawn.
  echo "wake-watchdog: $tmpl has $unread unread mail and no live session — spawning"
  gc session new "$tmpl" --no-attach 2>&1 | tail -1
  spawned=$((spawned + 1))
done

echo "wake-watchdog: spawned=$spawned no-mail=$skipped_no_mail already-live=$skipped_live"
