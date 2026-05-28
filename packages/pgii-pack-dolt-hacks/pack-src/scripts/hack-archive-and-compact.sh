#!/usr/bin/env bash
# hack-archive-and-compact.sh
#
# ────────────────────────────────────────────────────────────────────────
# THIS SCRIPT SHOULD NOT EXIST.  bd SHOULD SHIP A BUILT-IN ARCHIVE
# LIFECYCLE FOR HIGH-CHURN WORKLOADS.
# ────────────────────────────────────────────────────────────────────────
#
# Problem:
#   bd 1.0.4 stores every bd write as a separate dolt commit (~30 KB per
#   commit in noms). dolt's stats journal also grows independently (~1 GB
#   after a week). On this city's workload (~1800 task writes/hour),
#   the hq DB hit 2.7 GB in 5 days despite only ~10 MB of live data.
#   dolt's value (versioning, branches, time travel) is wasted on a
#   single-machine, no-remote workload like this one.
#
# What this script does each run:
#   1. Exports closed regular beads to JSONL files in
#      `archive/beads/<YYYY-MM-DD>.jsonl`, partitioned by close date.
#      Existing date files are appended-to-then-deduped so reruns are
#      idempotent.
#   2. Prunes ALL closed regular beads from the dolt DB (any age).
#   3. Runs `bd flatten --force` to squash commit history.
#   4. Runs `CALL DOLT_GC('--full')` to reclaim noms chunks.
#   5. Runs `CALL DOLT_STATS_PURGE()` to drop the stats journal
#      (stats regenerate as queries run).
#   6. git add archive/ + git commit (no push — no remote).
#
# After steady state, dolt contains only OPEN beads + a fresh single
# commit per day. The archive grows linearly in-repo as ~one daily JSONL
# file per active calendar day; git compresses subsequent commits well.
#
# Verified in sandbox 2026-05-18:
#   bd flatten             → 47,510 commits → 2
#   CALL DOLT_GC('--full') → frees orphaned noms chunks
#   CALL DOLT_STATS_PURGE  → frees ~1 GB stats journal
#   Net:                   → 2.7 GB → 14 MB, no bead data lost
#
# Coverage notes:
#   bd export excludes infra beads (sessions, messages, agents) by
#   default. Closed infra beads ARE pruned from dolt but NOT written to
#   the archive. We accept this — those are operational ephemera, not
#   work history. If you want them archived too, add --include-infra to
#   the export step below.
#
# Concurrency:
#   bd writes during the flatten window could in theory land in the old
#   branch and be lost. Mitigations: flatten is ~1 s in practice; the
#   city writes via batch mode so writes buffer in the working set,
#   not commits. If you want zero risk, suspend the city before manual
#   runs:  gc suspend; <run this script>; gc resume.
#
# ── Error handling policy ────────────────────────────────────────────
# Every step prints a progress line. If a step fails:
#   - Step 1 (export) is FATAL: without an archive we can't safely
#     destroy state downstream. Script aborts after printing AFTER
#     snapshot.
#   - Steps 2–6 (prune / flatten / GC / stats / git commit) are
#     non-fatal: the script logs the error and continues to the next
#     step. The exit code reflects whether all steps succeeded.
# Exit codes:
#   0 — all steps succeeded
#   1 — at least one non-fatal step failed
#   2 — fatal early abort (export failed)
# BEFORE and AFTER snapshots are always printed, regardless of how far
# the script got.
#
# ── Retirement criteria ──────────────────────────────────────────────
# Delete this script and orders/hack-archive-and-compact.toml when
# EITHER of the following lands:
#   1. bd ships a built-in archive + prune + compact lifecycle (export
#      closed beads, drop them, GC) so we don't have to assemble it.
#   2. The city moves off dolt to a backend whose storage cost is
#      proportional to live data (e.g., SQLite, JSONL-only). bd 1.0.x
#      has removed SQLite, so this likely requires a bd downgrade or
#      an upstream change.
# At retirement, the archive/ directory remains valid: it's plain JSONL
# and the closed beads can be re-imported with `bd import` if needed.
# =============================================================================

# Note: NOT using `set -e` — each step is wrapped in explicit error
# handling so we can complete as many steps as make sense.
set -uo pipefail

CITY_DIR="${GC_CITY:-/Users/phillipg/gc}"
ARCHIVE_DIR="$CITY_DIR/archive/beads"
DOLT_HOST="${BEADS_DOLT_SERVER_HOST:-127.0.0.1}"
DOLT_PORT="${BEADS_DOLT_SERVER_PORT:-24158}"
TS_UTC=$(date -u +%Y-%m-%dT%H:%M:%SZ)

mkdir -p "$ARCHIVE_DIR"

TMP=$(mktemp -t hack-archive-and-compact.XXXXXX)
trap 'rm -f "$TMP" "$TMP".closed "$TMP".dates' EXIT

# ──────────────────────────────────────────────────────────────────────
# Helpers
# ──────────────────────────────────────────────────────────────────────

ts() { date -u +%H:%M:%S; }
log() { echo "[$(ts)] $*"; }
err() {
  echo "[$(ts)] ERROR: $*" >&2
  HAD_FAILURES=1
}

# Track which steps ran and which failed
declare -a STEPS_OK=()
declare -a STEPS_FAILED=()
HAD_FAILURES=0
archived=0
committed=0

# Run a dolt SQL command, returning exit code, capturing output to var.
# Usage: dolt_sql "SQL_HERE" var_for_output
dolt_sql() {
  local sql="$1"
  DOLT_CLI_PASSWORD="${BEADS_DOLT_PASSWORD:-}" \
    dolt --host "$DOLT_HOST" --port "$DOLT_PORT" --user root --no-tls \
    sql -q "$sql" 2>&1
}

# Snapshot the city's storage / commit state. Resilient to partial
# unavailability — each measurement is independent.
snapshot() {
  local label="$1"
  echo
  echo "===== SNAPSHOT: $label ====="
  echo "  timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "  disk free:"
  df -h / 2>/dev/null | tail -1 | sed 's/^/    /'
  echo "  .beads/dolt: $(du -sh "$CITY_DIR/.beads/dolt" 2>/dev/null | awk '{print $1}')"
  echo "  .beads/dolt/hq: $(du -sh "$CITY_DIR/.beads/dolt/hq" 2>/dev/null | awk '{print $1}')"
  if [ -d "$CITY_DIR/.beads/dolt/hq/.dolt/noms/oldgen" ]; then
    echo "  hq/.dolt/noms/oldgen: $(du -sh "$CITY_DIR/.beads/dolt/hq/.dolt/noms/oldgen" 2>/dev/null | awk '{print $1}')"
  fi
  if [ -d "$CITY_DIR/.beads/dolt/hq/.dolt/stats" ]; then
    echo "  hq/.dolt/stats: $(du -sh "$CITY_DIR/.beads/dolt/hq/.dolt/stats" 2>/dev/null | awk '{print $1}')"
  fi
  # Dolt-side counts. If the server is busy / unreachable, fall through.
  local commits
  commits=$(dolt_sql "USE hq; SELECT COUNT(*) FROM dolt_log;" 2>/dev/null |
    grep -oE '[0-9]+' | head -1)
  echo "  hq dolt commits: ${commits:-?}"
  local issues
  issues=$(dolt_sql "USE hq; SELECT COUNT(*) FROM issues;" 2>/dev/null |
    grep -oE '[0-9]+' | head -1)
  local closed
  closed=$(dolt_sql "USE hq; SELECT COUNT(*) FROM issues WHERE status='closed';" 2>/dev/null |
    grep -oE '[0-9]+' | head -1)
  echo "  hq issues total / closed: ${issues:-?} / ${closed:-?}"
  echo "============================="
  echo
}

emit_summary() {
  echo
  echo "===== SUMMARY ====="
  echo "  archived to JSONL: $archived bead(s)"
  echo "  git committed:     $committed"
  echo "  steps succeeded:   ${STEPS_OK[*]:-(none)}"
  echo "  steps failed:      ${STEPS_FAILED[*]:-(none)}"
  echo "  exit code:         $HAD_FAILURES"
  echo "==================="
}

# ──────────────────────────────────────────────────────────────────────
# Main
# ──────────────────────────────────────────────────────────────────────

log "hack-archive-and-compact starting (city=$CITY_DIR, dolt=$DOLT_HOST:$DOLT_PORT)"

snapshot "BEFORE"

# ── Step 1: export closed beads to JSONL (FATAL on failure) ─────────
log "STEP 1: bd export → JSONL, partition by close date"
if ! bd export 2>"$TMP.err" >"$TMP"; then
  err "STEP 1: bd export failed:"
  sed 's/^/    /' "$TMP.err" >&2 || true
  err "Cannot safely continue: without an archive, downstream prune would lose data."
  STEPS_FAILED+=(export)
  HAD_FAILURES=2
  snapshot "AFTER (aborted at STEP 1)"
  emit_summary
  exit "$HAD_FAILURES"
fi
log "  bd export ok ($(wc -l <"$TMP" | tr -d ' ') lines)"

# Partition by close date. If jq fails on the entire input, we abort
# (we'd have nothing to archive). If individual records have malformed
# close_at, jq's select() simply drops them — acceptable.
if ! jq -c 'select(.status == "closed")' "$TMP" >"$TMP.closed" 2>"$TMP.err"; then
  err "STEP 1: jq filter failed:"
  sed 's/^/    /' "$TMP.err" >&2 || true
  err "Cannot safely continue: archive not written."
  STEPS_FAILED+=(export)
  HAD_FAILURES=2
  snapshot "AFTER (aborted at STEP 1)"
  emit_summary
  exit "$HAD_FAILURES"
fi

if [ -s "$TMP.closed" ]; then
  jq -r '(.closed_at // "")[0:10]' "$TMP.closed" | sort -u | awk 'NF' >"$TMP.dates"
  while IFS= read -r date; do
    [ -z "$date" ] && continue
    out="$ARCHIVE_DIR/$date.jsonl"
    jq -c --arg d "$date" \
      'select((.closed_at // "")[0:10] == $d)' \
      "$TMP.closed" >>"$out"
    awk '!seen[$0]++' "$out" >"$out.tmp" && mv "$out.tmp" "$out"
  done <"$TMP.dates"
  archived=$(wc -l <"$TMP.closed" | tr -d ' ')
fi
log "  STEP 1 ok: archived=$archived closed bead(s) to $ARCHIVE_DIR/"
STEPS_OK+=(export)

# ── Step 2: prune closed beads from dolt (continuable) ──────────────
log "STEP 2: bd prune --pattern '*' --force"
if bd prune --pattern '*' --force 2>"$TMP.err"; then
  log "  STEP 2 ok"
  STEPS_OK+=(prune)
else
  err "STEP 2: bd prune failed:"
  sed 's/^/    /' "$TMP.err" >&2 || true
  err "Continuing — flatten/GC may still partially compact."
  STEPS_FAILED+=(prune)
fi

# ── Step 3: flatten dolt commit history (continuable, was the silent failure) ──
log "STEP 3: bd flatten --force"
if bd flatten --force 2>"$TMP.err"; then
  log "  STEP 3 ok"
  STEPS_OK+=(flatten)
else
  err "STEP 3: bd flatten failed:"
  sed 's/^/    /' "$TMP.err" >&2 || true
  err "Continuing — GC will only reclaim chunks orphaned by prune, not full history."
  STEPS_FAILED+=(flatten)
fi

# ── Step 4: DOLT_GC --full (continuable) ────────────────────────────
log "STEP 4: CALL DOLT_GC('--full')"
if dolt_sql "USE hq; CALL DOLT_GC('--full');" >"$TMP.err" 2>&1; then
  log "  STEP 4 ok"
  STEPS_OK+=(dolt_gc)
else
  err "STEP 4: CALL DOLT_GC('--full') failed:"
  sed 's/^/    /' "$TMP.err" >&2 || true
  err "Continuing — stats purge can still run."
  STEPS_FAILED+=(dolt_gc)
fi

# ── Step 5: DOLT_STATS_PURGE (continuable) ──────────────────────────
log "STEP 5: CALL DOLT_STATS_PURGE()"
if dolt_sql "USE hq; CALL DOLT_STATS_PURGE();" >"$TMP.err" 2>&1; then
  log "  STEP 5 ok"
  STEPS_OK+=(stats_purge)
else
  err "STEP 5: CALL DOLT_STATS_PURGE failed:"
  sed 's/^/    /' "$TMP.err" >&2 || true
  err "Continuing — stats journal not reclaimed this run."
  STEPS_FAILED+=(stats_purge)
fi

# ── Step 6: git commit the archive (continuable) ────────────────────
log "STEP 6: git add archive/ && git commit"
if [ -d "$CITY_DIR/.git" ]; then
  cd "$CITY_DIR"
  if ! git add archive/ 2>"$TMP.err"; then
    err "STEP 6: git add archive/ failed:"
    sed 's/^/    /' "$TMP.err" >&2 || true
    STEPS_FAILED+=(git_commit)
  elif git diff --cached --quiet archive/ 2>/dev/null; then
    log "  STEP 6 ok (no archive changes to commit)"
    STEPS_OK+=(git_commit)
  elif git commit -m "archive(beads): $archived closed bead(s) @ $TS_UTC

Auto-emitted by hack-archive-and-compact. Closed beads JSONL is the
durable history; dolt is compacted after each run. See HACKS.md." \
    >"$TMP.err" 2>&1; then
    log "  STEP 6 ok"
    committed=1
    STEPS_OK+=(git_commit)
  else
    err "STEP 6: git commit failed:"
    sed 's/^/    /' "$TMP.err" >&2 || true
    STEPS_FAILED+=(git_commit)
  fi
else
  err "STEP 6: $CITY_DIR is not a git repo; skipping commit."
  STEPS_FAILED+=(git_commit)
fi

# ── Final snapshot + summary ────────────────────────────────────────
snapshot "AFTER"
emit_summary

exit "$HAD_FAILURES"
