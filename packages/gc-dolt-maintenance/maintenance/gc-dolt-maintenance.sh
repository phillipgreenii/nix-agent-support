# shellcheck shell=bash
CITY="${GC_CITY:-${CITY_PATH:-$PWD}}"
STATE_DIR="${STATE_DIR:-${GC_HOME:-$HOME/.gc}/dolt-maintenance}"
COMMIT_THR="${FLATTEN_COMMIT_THRESHOLD:-5000}"
BUSY_THR="${BUSY_PROC_THRESHOLD:-4}"
MIN_H="${MIN_FLATTEN_INTERVAL_HOURS:-6}"
MAX_H="${MAX_FLATTEN_INTERVAL_HOURS:-24}"
DO_FLATTEN=1
DOLT_ROOT="$CITY/.beads/dolt"

log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

breaker_applied() {
  local f="$CITY/.beads/issues.jsonl"
  [[ -e $f && "$(/usr/bin/stat -f '%z' "$f" 2>/dev/null)" == "0" ]] &&
    /usr/bin/stat -f '%Sf' "$f" 2>/dev/null | grep -q uchg
}

while [[ $# -gt 0 ]]; do case $1 in
  --no-flatten)
    DO_FLATTEN=0
    shift
    ;;
  --city)
    CITY=$2
    DOLT_ROOT="$CITY/.beads/dolt"
    shift 2
    ;;
  -h | --help)
    echo "gc-dolt-maintenance [--city DIR] [--no-flatten]"
    exit 0
    ;;
  *)
    echo "unknown arg: $1" >&2
    exit 1
    ;;
  esac done

mkdir -p "$STATE_DIR"
log "=== gc-dolt-maintenance start (city=$CITY) ==="
ba=0
breaker_applied && ba=1
otlp_gauge dolt_maint_breaker_applied "$ba"
otlp_gauge dolt_maint_busy_procs "$(pgrep -fc 'bin/bd ' || echo 0)"

for dbdir in "$DOLT_ROOT"/*/; do
  [[ -d "$dbdir/.dolt" ]] || continue
  db=$(basename "$dbdir")
  before=$(du -sm "$dbdir" 2>/dev/null | cut -f1)

  for proc in DOLT_STATS_PURGE DOLT_GC; do
    if (cd "$dbdir" && dolt sql -q "CALL $proc()") >/dev/null 2>&1; then
      log "$db: $proc ok"
      otlp_log INFO "$proc ok" db="$db"
    else
      log "$db: $proc failed"
      otlp_log WARN "$proc failed" db="$db"
    fi
  done
  after=$(du -sm "$dbdir" 2>/dev/null | cut -f1)
  log "$db: ${before:-?}MB -> ${after:-?}MB"

  commit=$(cd "$dbdir" && dolt sql -r csv -q "SELECT COUNT(*) FROM dolt_log" 2>/dev/null | tail -1)
  commit=${commit:-0}
  size_bytes=$((${after:-0} * 1024 * 1024))
  remote=0
  (cd "$dbdir" && [[ -n "$(dolt remote -v 2>/dev/null)" ]]) && remote=1
  busy=$(pgrep -fc 'bin/bd ' || echo 0)
  statef="$STATE_DIR/$db.last-flatten"
  if [[ -f $statef ]]; then hours=$((($(date +%s) - $(cat "$statef")) / 3600)); else hours=999999; fi
  ba=0
  breaker_applied && ba=1

  otlp_gauge dolt_maint_commit_count "$commit" db="$db"
  otlp_gauge dolt_maint_size_bytes "$size_bytes" db="$db"
  otlp_gauge dolt_maint_hours_since_flatten "$hours" db="$db"
  otlp_gauge dolt_maint_has_remote "$remote" db="$db"

  if [[ $DO_FLATTEN == 1 ]]; then
    decision=$(should_flatten "$commit" "$busy" "$hours" "$ba" "$remote" "$COMMIT_THR" "$BUSY_THR" "$MIN_H" "$MAX_H")
    log "$db: flatten decision=$decision (commit=$commit busy=$busy hours=$hours)"
    otlp_log INFO "flatten_decision ${decision%%:*}" db="$db" reason="${decision#*:}" commit_count="$commit"
    if [[ $decision == yes:* ]]; then
      if (cd "$CITY" && gc bd flatten --force) >/dev/null 2>&1; then
        date +%s >"$statef"
        log "$db: flatten ok"
        otlp_log INFO "flatten_exec ok" db="$db"
      else
        log "$db: flatten failed (non-fatal)"
        otlp_log ERROR "flatten_exec failed" db="$db"
      fi
    fi
  fi
done
otlp_gauge dolt_maint_last_run_timestamp "$(date +%s)"
log "=== gc-dolt-maintenance done ==="
