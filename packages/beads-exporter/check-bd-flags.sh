#!/usr/bin/env bash
# Checks a bd package against the beads-exporter's recorded expectations.
#
# Usage: check-bd-flags.sh <path-to-bd> <testdata/bd directory>
#
# 1. `bd version` must equal the recorded VERSION pin. A bd bump fails here
#    until the contract suite is re-run with -update.
# 2. Every flag in flags.txt must appear in the matching subcommand's help
#    (global flags are listed in every subcommand's help).
#
# Exit codes: 0 all good; 2 usage; 3 version mismatch; 4 a flag is missing.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-bd-flags.sh <path-to-bd> <testdata/bd directory>" >&2
  exit 2
fi
bd="$1"
data="$2"

scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT
# bd runs with an empty home and no beads directory; PATH is kept because a
# wrapped bd package needs bash and coreutils to start.
run_bd() {
  env -i HOME="${scratch}" PATH="${PATH}" BEADS_DOLT_AUTO_START=0 BD_BACKUP_ENABLED=0 "${bd}" "$@"
}

want="$(tr -d '[:space:]' <"${data}/VERSION")"
have="$(run_bd version | sed -n 's/^bd version \([^ ]*\).*/\1/p' | head -n 1)"
if [ -z "${have}" ]; then
  echo "FAIL: could not read the version from '${bd} version'" >&2
  exit 3
fi
if [ "${have}" != "${want}" ]; then
  echo "FAIL: bd version is ${have}, the recorded pin is ${want}." >&2
  echo "      Re-run the beads-exporter contract suite against this bd with -update" >&2
  echo "      (it re-records testdata/bd and VERSION), review the diff, then commit." >&2
  exit 3
fi

missing=0
while read -r sub flag; do
  case "${sub}" in '' | '#'*) continue ;; esac
  help_sub="${sub}"
  [ "${sub}" = "*" ] && help_sub="list"
  help="$(run_bd "${help_sub}" --help 2>&1)" || {
    echo "FAIL: '${bd} ${help_sub} --help' failed" >&2
    missing=1
    continue
  }
  # A flag line looks like "      --all  ..." or "  -n, --limit int ...".
  case "${flag}" in
  --*) pattern="^[[:space:]]+(-[A-Za-z], )?${flag}([[:space:]]|\$)" ;;
  -?) pattern="^[[:space:]]+${flag}, " ;;
  *)
    echo "FAIL: malformed flag '${flag}' in flags.txt" >&2
    missing=1
    continue
    ;;
  esac
  if ! printf '%s\n' "${help}" | grep -Eq -- "${pattern}"; then
    echo "FAIL: bd ${sub} has no flag ${flag}" >&2
    missing=1
  fi
done <"${data}/flags.txt"

if [ "${missing}" -ne 0 ]; then
  exit 4
fi
echo "OK: bd ${have} matches the pin and provides every recorded flag."
