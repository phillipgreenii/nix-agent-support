#!/usr/bin/env bash
# hack-stale-lock-sweeper.sh
#
# ────────────────────────────────────────────────────────────────────────
# THIS SCRIPT SHOULD NOT EXIST. GIT OPERATIONS THAT GET KILLED MID-FLIGHT
# SHOULDN'T BE LEAVING STALE INDEX.LOCK FILES.
# ────────────────────────────────────────────────────────────────────────
#
# Background: multiple HACK orders + the bd auto-import + worker git
# operations all touch git repos under the city tree and the registered
# rig trees. When one of these gets killed mid-`git commit` (timeout,
# OOM, disk pressure, SIGTERM, concurrent fire), the killed git process
# leaves a `.git/index.lock` file behind. Subsequent commits to the same
# repo then fail with:
#
#   fatal: Unable to create '<path>/.git/index.lock': File exists.
#
# Cascades observed 2026-05-20 through 2026-05-27:
#   - /Volumes/ziprecruiter/monorepo/.git/index.lock — blocks worker
#     pushes; surfaces as 'step ssh proxycommand silently fails' in
#     worker escalations (the worktree's underlying index is corrupted
#     so no .envrc / bin/step is discoverable).
#   - /Users/phillipg/gc/.git/index.lock — blocks mayor commits +
#     hack-archive-and-compact daily archival.
#   - /Users/phillipg/gc/.gc/runtime/packs/pgii-dolt-hacks/jsonl-archive/.git/index.lock —
#     blocks HACK 11 (hack-mol-dog-jsonl) push cycles, which then
#     pump ESCALATION: JSONL push failed mail until cleared.
#
# This script runs every 5 minutes against the city tree and every
# registered rig tree. For each `.git/index.lock` found:
#   1. If younger than $STALE_SECONDS, skip (still might be live).
#   2. If any process holds it via lsof, skip.
#   3. Otherwise, remove and log.
#
# Defense-in-depth alongside HACK 11's wrapper-level EXIT trap (which
# only catches its own kills). This catches locks from ANY source —
# bd auto-import, hack-archive-and-compact, worker `git mu`, dolt
# operations, the operator running ad-hoc git, etc.
#
# Logs to $GC_CITY/.cache/hack-stale-lock-sweeper/run-<ts>.log.
#
# Retire when: gascity orders stop getting killed mid-git-commit
# (proper timeouts + concurrency control upstream) AND no other source
# is producing stale locks across the city's git repos.

set -euo pipefail

CITY="${GC_CITY:-$HOME/gc}"
CACHE="$CITY/.cache/hack-stale-lock-sweeper"
STALE_SECONDS="${SWEEPER_STALE_SECONDS:-300}" # 5 min

mkdir -p "$CACHE"
RUN_LOG="$CACHE/run-$(date +%s).log"
exec > >(tee "$RUN_LOG") 2>&1

log() { echo "[$(date -Iseconds)] $*"; }

if [[ -f "$CITY/QUOTA_PAUSED" ]]; then
  log "QUOTA_PAUSED — exiting"
  exit 0
fi

# Build scan list: the city + every registered rig path that exists.
SCAN_PATHS=("$CITY")
while IFS= read -r rig_path; do
  [ -n "$rig_path" ] && [ -d "$rig_path" ] && SCAN_PATHS+=("$rig_path")
done < <(gc rig list 2>/dev/null | awk '/Path:/ { for (i=2; i<=NF; i++) printf "%s%s", $i, (i<NF?" ":""); print "" }')

NOW_EPOCH=$(date +%s)
total_found=0
total_removed=0
total_skipped_fresh=0
total_skipped_held=0
total_failed=0

mtime_of() {
  # Portable across BSD (macOS) and GNU. Falls back to 0 on any failure
  # or non-numeric output so the caller's arithmetic doesn't blow up
  # under `set -u` (which treats non-numeric strings in $(()) as
  # unbound variable refs).
  local m
  m=$(date -r "$1" +%s 2>/dev/null || true)
  [[ $m =~ ^[0-9]+$ ]] && printf '%s' "$m" || printf '0'
}

check_lock() {
  local lock="$1"
  [ -f "$lock" ] || return 0
  total_found=$((total_found + 1))

  local lock_mtime lock_age
  lock_mtime=$(mtime_of "$lock")
  lock_age=$((NOW_EPOCH - lock_mtime))

  if [ "$lock_age" -lt "$STALE_SECONDS" ]; then
    log "  FRESH (${lock_age}s) — skipping: $lock"
    total_skipped_fresh=$((total_skipped_fresh + 1))
    return 0
  fi

  if lsof "$lock" >/dev/null 2>&1; then
    log "  HELD — skipping: $lock"
    total_skipped_held=$((total_skipped_held + 1))
    return 0
  fi

  if rm -f "$lock" 2>/dev/null; then
    log "  REMOVED (${lock_age}s stale): $lock"
    total_removed=$((total_removed + 1))
  else
    log "  WARN: rm failed for $lock"
    total_failed=$((total_failed + 1))
  fi
}

for root in "${SCAN_PATHS[@]}"; do
  log "scanning $root"
  # Find .git directories first, prune to stop descent (don't walk into
  # .git/objects/ etc.).
  #
  # Depth is tuned per root: the city needs depth 8 to catch nested
  # archive repos under .gc/runtime/packs/<pack>/jsonl-archive/.git
  # (depth 5) and worker worktrees at .gc/worktrees/<rig>/<role>/<wt>/.git
  # (depth 5). Rig roots only need depth 2 — the rig's own .git is at
  # depth 1, and any linked worktree's index.lock lives at
  # <rig>/.git/worktrees/<wt>/index.lock which we enumerate explicitly
  # below (we don't need find to descend into .git/ at all).
  #
  # Without this restriction, the ZR monorepo scan walks ~200k files
  # and takes ~3.5min — close to the order timeout.
  if [ "$root" = "$CITY" ]; then
    depth=8
  else
    depth=2
  fi
  while IFS= read -r gitdir; do
    # Canonical repo lock
    check_lock "$gitdir/index.lock"
    # Worktree-specific locks (if this is a main repo hosting linked
    # worktrees, each has its own index under .git/worktrees/<wt>/).
    if [ -d "$gitdir/worktrees" ]; then
      for wt in "$gitdir/worktrees"/*/index.lock; do
        check_lock "$wt"
      done
    fi
  done < <(find "$root" -maxdepth "$depth" -type d -name ".git" -prune 2>/dev/null)
done

log "scan complete — found=$total_found removed=$total_removed skipped_fresh=$total_skipped_fresh skipped_held=$total_skipped_held failed=$total_failed"
