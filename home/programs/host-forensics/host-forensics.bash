#!/usr/bin/env bash
# host-forensics: keep the evidence an unclean reboot would otherwise erase.
#   host-forensics panics     copy new kernel panic reports into the retained directory
#   host-forensics snapshot   append one line of free disk, swap, load and wired memory, once per local day
#   host-forensics run        panics, then snapshot (what the launchd agent runs)
# Environment (all optional; tests point them at a fixture directory):
#   HF_DIAG_DIR   where the system writes panic reports  (/Library/Logs/DiagnosticReports)
#   HF_STATE_DIR  retained evidence root                 (${XDG_STATE_HOME:-$HOME/.local/state}/host-forensics)
# Exit codes: 0 ok, 2 usage error.
set -euo pipefail

# launchd's PATH lacks the sbin directories that hold sysctl; fixtures put stubs first.
PATH="$PATH:/usr/sbin:/sbin"

diag_dir="${HF_DIAG_DIR:-/Library/Logs/DiagnosticReports}"
state_dir="${HF_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/host-forensics}"

log() { printf '%s host-forensics: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# panics: a report already retained is never touched again, so only reports the
# system has created since the last run are copied.
panics() {
  local dest="$state_dir/panics" f name copied=0
  mkdir -p "$dest"
  if [ ! -d "$diag_dir" ]; then
    log "no diagnostic directory $diag_dir; nothing to copy"
    return 0
  fi
  for f in "$diag_dir"/*.panic; do
    [ -f "$f" ] || continue
    name="$(basename "$f")"
    [ -e "$dest/$name" ] && continue
    if cp -p "$f" "$dest/$name"; then
      log "copied panic report $name"
      copied=$((copied + 1))
    else
      log "could not copy panic report $name"
    fi
  done
  log "panics: $copied new"
}

# snapshot: a tab-separated line, date first, so "already taken today" is a prefix test.
snapshot() {
  local file="$state_dir/snapshots.tsv" today now disk_kb swap load wired page vm
  mkdir -p "$state_dir"
  today="$(date +%Y-%m-%d)"
  if [ -f "$file" ] && tail -n 1 "$file" | grep -q "^$today"; then
    log "snapshot: already taken for $today"
    return 0
  fi
  now="$(date +%Y-%m-%dT%H:%M:%S%z)"
  disk_kb="$(df -k / | awk 'NR==2 {print $4}')"
  swap="$(sysctl -n vm.swapusage 2>/dev/null | tr -s ' \t' ' ' || true)"
  load="$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | tr -s ' ' | sed 's/^ //; s/ $//' || true)"
  vm="$(vm_stat 2>/dev/null || true)"
  page="$(printf '%s\n' "$vm" | awk '/page size of/ {print $8}')"
  wired="$(printf '%s\n' "$vm" | awk '/Pages wired down/ {gsub(/\./, "", $4); print $4}')"
  if [ -n "$wired" ] && [ -n "$page" ]; then
    wired="$((wired * page))"
  else
    wired=""
  fi
  if [ ! -s "$file" ]; then
    printf '# time\tfree_disk_kb\tswap\tload\twired_bytes\n' >>"$file"
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$now" "$disk_kb" "$swap" "$load" "$wired" >>"$file"
  log "snapshot: recorded $now"
}

case "${1:-}" in
panics) panics ;;
snapshot) snapshot ;;
run)
  panics
  snapshot
  ;;
*)
  echo "usage: host-forensics panics|snapshot|run" >&2
  exit 2
  ;;
esac
