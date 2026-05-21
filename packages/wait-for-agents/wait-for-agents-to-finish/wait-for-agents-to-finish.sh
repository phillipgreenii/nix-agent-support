# shellcheck shell=bash
# wait-for-agents-to-finish - thin wrapper delegating to pa-monitor.

set -euo pipefail

args=(--wait-until-idle)

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
    args+=(--time-between-checks "$2")
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
    args+=(--caffeinate)
    shift
    ;;
  -h | --help)
    cat <<EOF
Usage: wait-for-agents-to-finish [OPTIONS]

Thin wrapper around pa-monitor --wait-until-idle. Options:
  --maximum-wait SECONDS
  --time-between-checks SECS
  --consecutive-idle-checks N
  --caffeinate
  -h, --help
  -v, --version
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

exec pa-monitor "${args[@]}"
