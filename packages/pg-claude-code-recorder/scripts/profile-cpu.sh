#!/usr/bin/env bash
# Measure the pg-claude-code-recorder daemon's CPU over a window with the pprof
# recipe (docs/go-daemon-profiling-runbook.md in phillipg-nix-repo-base): fetch
# /debug/pprof/profile?seconds=N over the opt-in pprof unix socket with curl,
# read it with `go tool pprof -top`, and print the CPU as a percentage of one
# core. The real-tree verification runs it for 600 seconds against the deployed
# daemon; a developer runs it against a throwaway daemon started with
# `pg-claude-code-recorder serve --pprof`.
#
# Phase 1 bounds (not derived, re-measure): the copier and watcher alone at or
# below 5% of one core averaged over 10 minutes on the real tree with no client
# attached, the whole daemon at or below 10% once the parser and feed run.
#
# Acceptance of the whole-daemon bound: a 900 second window
# (profile-cpu.sh --seconds 900 --pid <daemon pid>) on a quiet host with the usual
# clients attached (pa-monitor-daemon, ccpool) reports cpu_percent_of_one_core at
# or below 10. The bound covers client query load; it is not restated to exclude it.
set -euo pipefail

usage() {
  cat <<'USAGE'
usage: profile-cpu.sh [--sock PATH] [--seconds N] [--pid PID] [--out DIR]

  --sock PATH    the daemon's pprof socket
                 (default $XDG_STATE_HOME/pg-claude-code-recorder/pprof.sock)
  --seconds N    window length in seconds (default 600; the request blocks N seconds)
  --pid PID      also cross-check with the process CPU time from ps over the same window
  --out DIR      keep the profile here (default: a new temporary directory)

Prints key=value lines; cpu_percent_of_one_core is the figure to compare with
the bound. Environment: CURL (default curl) and PPROF (default "go tool pprof")
name the tools. Never commit a captured profile.
USAGE
}

sock="${XDG_STATE_HOME:-$HOME/.local/state}/pg-claude-code-recorder/pprof.sock"
seconds=600
pid=""
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
  --sock)
    sock="${2:?--sock needs a path}"
    shift 2
    ;;
  --seconds)
    seconds="${2:?--seconds needs a number}"
    shift 2
    ;;
  --pid)
    pid="${2:?--pid needs a pid}"
    shift 2
    ;;
  --out)
    out="${2:?--out needs a directory}"
    shift 2
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "profile-cpu.sh: unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
  esac
done
case "$seconds" in
'' | *[!0-9]*)
  echo "profile-cpu.sh: --seconds must be a whole number" >&2
  exit 2
  ;;
esac

curl_cmd="${CURL:-curl}"
pprof_cmd="${PPROF:-go tool pprof}"

if [ ! -S "$sock" ]; then
  echo "profile-cpu.sh: no pprof socket at $sock; start the daemon with --pprof (or set PG_CLAUDE_CODE_RECORDER_PPROF_ADDR)" >&2
  exit 3
fi
if [ -z "$out" ]; then
  out="$(mktemp -d "${TMPDIR:-/tmp}/ccrec-profile.XXXXXX")"
else
  mkdir -p "$out"
fi

# cpu_seconds PID prints the process CPU time in seconds ([dd-][hh:]mm:ss[.cc]).
cpu_seconds() {
  ps -o time= -p "$1" | awk '{
    n = split($1, d, "-"); days = 0; t = $1
    if (n == 2) { days = d[1]; t = d[2] }
    m = split(t, p, ":"); s = 0
    for (i = 1; i <= m; i++) s = s * 60 + p[i]
    printf "%.2f\n", days * 86400 + s
  }'
}

cpu_before=""
if [ -n "$pid" ]; then
  cpu_before="$(cpu_seconds "$pid")"
fi

# No client timeout: --max-time shorter than N would cut the capture off.
$curl_cmd --fail --silent --show-error --unix-socket "$sock" \
  "http://pprof/debug/pprof/profile?seconds=$seconds" -o "$out/cpu.pb.gz"

# shellcheck disable=SC2086 # PPROF is a command with arguments
top="$($pprof_cmd -top -nodecount=15 "$out/cpu.pb.gz" 2>/dev/null)"
printf '%s\n' "$top" >"$out/top.txt"

# "Duration: 10s, Total samples = 120ms ( 1.20%)": the percentage is samples
# over the window, that is the share of one core.
percent="$(printf '%s\n' "$top" | grep -E 'Total samples' | grep -Eo '\( *[0-9.]+%\)' | grep -Eo '[0-9.]+' | head -n 1 || true)"
percent="${percent:-0.00}"

echo "window_seconds=$seconds"
echo "cpu_percent_of_one_core=$percent"
if [ -n "$pid" ]; then
  cpu_after="$(cpu_seconds "$pid")"
  echo "ps_cpu_percent_of_one_core=$(awk -v a="$cpu_before" -v b="$cpu_after" -v w="$seconds" 'BEGIN { if (w > 0) printf "%.2f\n", (b - a) * 100 / w; else print "0.00" }')"
fi
echo "profile=$out/cpu.pb.gz"
echo "top=$out/top.txt"
