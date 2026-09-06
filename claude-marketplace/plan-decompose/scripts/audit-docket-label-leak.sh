#!/usr/bin/env bash
# audit-docket-label-leak.sh
#
# Finds beads that carry the plan-decompose `docket` label but are NOT
# docket-shaped (issue_type != epic). Every legitimate holder of the
# `docket` label -- a docket epic (`create-docket`) or a phase bead
# (epic-decompose's phase-bead usage of `create-docket`) -- is bd type
# `epic`. A work-packet bead (`create-packet`, bd type `task`, or any other
# non-epic type) picks up `docket` ONLY when its creation omitted
# `--no-inherit-labels`, letting it inherit the label from its parent docket
# epic. That omission breaks `find-docket`'s label-based epic scan (see
# plan-decompose-beads/SKILL.md's `create-packet` section) and has recurred
# more than once in practice. Nothing previously re-checked for it after
# packet creation; this script is that periodic audit.
#
# Read-only: it only calls `bd list`, never mutates bead state.
set -euo pipefail

show_help() {
  cat <<'HELP'
audit-docket-label-leak.sh: find packet-shaped beads that leaked the `docket` label

Usage: audit-docket-label-leak.sh [OPTIONS]

Runs `bd list --label docket --status all -n 0 --json` and flags every
returned bead whose issue_type is not `epic` -- a packet (or any other
non-epic bead) that inherited its parent docket epic's `docket` label
because a `bd create` for it omitted `--no-inherit-labels`.

Exit codes:
  0  no leaks found
  2  usage error
  3  leaks found (see output for the offending bead ids)
  1  any other failure (bd/jq missing, `bd list` itself failed, ...)

Options:
  -j, --json    Emit the leaked beads as a JSON array (id, issue_type, title)
                instead of a human-readable list
  -h, --help    Show this help message
HELP
}

die() {
  echo "audit-docket-label-leak.sh: error: $1" >&2
  exit "${2:-1}"
}

JSON_OUT=0
ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -j | --json)
    JSON_OUT=1
    shift
    ;;
  --)
    shift
    ARGS+=("$@")
    break
    ;;
  -*)
    die "unknown option: $1" 2
    ;;
  *)
    ARGS+=("$1")
    shift
    ;;
  esac
done

[[ ${#ARGS[@]} -eq 0 ]] || die "audit-docket-label-leak.sh takes no positional arguments, got: ${ARGS[*]}" 2

command -v bd >/dev/null 2>&1 || die "bd not found on PATH"
command -v jq >/dev/null 2>&1 || die "jq not found on PATH"

RAW="$(bd list --label docket --status all -n 0 --json)" || die "bd list --label docket failed"

LEAKS="$(jq -c '[.data[] | select(.issue_type != "epic") | {id, issue_type, title}]' <<<"$RAW")" ||
  die "failed to parse bd list output as JSON"

COUNT="$(jq 'length' <<<"$LEAKS")"

if [[ $JSON_OUT -eq 1 ]]; then
  printf '%s\n' "$LEAKS"
elif [[ $COUNT -eq 0 ]]; then
  echo "audit-docket-label-leak.sh: no leaks found"
else
  echo "audit-docket-label-leak.sh: $COUNT bead(s) carry the docket label but are not docket-shaped (issue_type != epic):"
  jq -r '.[] | "  " + .id + " (" + .issue_type + "): " + .title' <<<"$LEAKS"
fi

[[ $COUNT -eq 0 ]] || exit 3
