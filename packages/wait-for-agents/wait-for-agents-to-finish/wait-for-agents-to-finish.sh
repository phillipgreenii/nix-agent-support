# shellcheck shell=bash
# wait-for-agents-to-finish - thin wrapper delegating to pa-monitor.

set -euo pipefail

# ADR 0011 replaced pa-monitor's `--wait-until-idle` FLAG with the
# `wait-until-agents-finished` SUBCOMMAND, so the subcommand token MUST be the
# FIRST argument: pickSubcommand (cmd/pa-monitor/main.go) routes a LEADING flag
# to `tui`, whose flag set defines only `-version` and would reject the wait
# options outright.
args=(wait-until-agents-finished)

# `--caffeinate` has no counterpart on `wait-until-agents-finished`: pa-monitor's
# caffeinate is daemon-owned global state toggled by `pa-monitor caffeinate
# on|off|toggle`, which this wait must not mutate on the caller's behalf. Keep
# the documented UX by running the system `caffeinate` against this pid instead,
# as agent-activity-api's `wait` does.
use_caffeinate=false

while [[ $# -gt 0 ]]; do
  case "$1" in
  --maximum-wait)
    if [[ -z ${2:-} ]]; then
      echo "Error: --maximum-wait requires a value" >&2
      exit 2
    fi
    args+=(--maximum-wait "$2")
    shift 2
    ;;
  --time-between-checks)
    if [[ -z ${2:-} ]]; then
      echo "Error: --time-between-checks requires a value" >&2
      exit 2
    fi
    # Still accepted so existing callers do not break, but NOT forwarded:
    # `wait-until-agents-finished` observes the daemon's WatchState push stream
    # at a fixed interval, so pa-monitor defines no poll-interval option to map
    # this onto.
    echo "Warning: --time-between-checks is ignored; pa-monitor's wait is driven by the daemon's push stream" >&2
    shift 2
    ;;
  --consecutive-idle-checks)
    if [[ -z ${2:-} ]]; then
      echo "Error: --consecutive-idle-checks requires a value" >&2
      exit 2
    fi
    args+=(--consecutive-idle-checks "$2")
    shift 2
    ;;
  --caffeinate)
    use_caffeinate=true
    shift
    ;;
  -h | --help)
    cat <<EOF
Usage: wait-for-agents-to-finish [OPTIONS]

Thin wrapper around \`pa-monitor wait-until-agents-finished\`. Options:
  --maximum-wait SECONDS
  --time-between-checks SECS   (accepted but IGNORED; no pa-monitor counterpart)
  --consecutive-idle-checks N
  --caffeinate
  -h, --help
  -v, --version

Exit codes:
  0  idle reached (no agent is actively running a turn)
  1  timeout (--maximum-wait elapsed)
  2  error (invalid arguments, daemon unavailable)

NOTE: exit 0 means "idle reached", NOT "work finished". Only a 'working'
session counts as busy (pa-monitor ADR 0024 R3), so a session BLOCKED on a
5h/weekly usage limit counts as idle and does not hold this wait open -- it
will resume on its own at the window reset. If you must not proceed until
pending work is genuinely done, also check the 'blocked' count in
\`pa-monitor status\` and re-wait after the window resets.
EOF
    exit 0
    ;;
  -v | --version)
    exec pa-monitor --version
    ;;
  *)
    echo "Error: Unknown option: $1" >&2
    exit 2
    ;;
  esac
done

if [[ $use_caffeinate == true ]] && command -v caffeinate >/dev/null 2>&1; then
  # `-w $$` watches THIS pid; the exec below keeps the same pid, so caffeinate
  # exits when the wait does.
  caffeinate -w $$ &
fi

exec pa-monitor "${args[@]}"
