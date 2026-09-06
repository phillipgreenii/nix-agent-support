#!/usr/bin/env bash
# create-packet.sh
#
# Wraps `bd create` for plan-decompose's create-packet operation with
# --no-inherit-labels baked in as the DEFAULT -- never a flag a caller has
# to remember to type. create-packet's own --no-inherit-labels flag has
# been omitted from hand-typed `bd create` invocations more than once
# (pg2-2j5ac's children `.5`-`.10`, then independently `pg2-84o3m.31`),
# each time letting the new packet inherit its parent docket epic's
# `docket` label (or, on a `human`-labeled parent, that label too) and
# break `find-docket`'s label-based epic scan (see
# plan-decompose-beads/SKILL.md's `create-packet` section, and
# `scripts/audit-docket-label-leak.sh`, the after-the-fact check for this
# same defect). A prose reminder alone recurred through that omission
# twice even with the flag written directly into the documented example;
# this script makes the default STRUCTURAL instead -- the flag is baked
# into the command this script runs, not retyped by hand at each call
# site.
#
# Creates one work-packet bead under --parent, defers it immediately
# afterward (HELD is status-based, per create-packet's own convention),
# and prints the new bead id to stdout.
set -euo pipefail

show_help() {
  cat <<'HELP'
create-packet.sh: create one plan-decompose work-packet bead, safely

Usage:
  create-packet.sh --parent <docket-id> --title <title> --body-file <file>
                    (--acceptance <text> | --acceptance-file <file>)
                    [--metadata <json>] [--label <labels>]
                    [--allow-inherit-labels]

Runs, in order:
  bd create "<title>" -t task --parent <docket-id> --no-inherit-labels \
    --body-file <file> --acceptance "<text>" [--metadata <json>] \
    [--labels <labels>] --silent
  bd defer <new-id>

--no-inherit-labels is ALWAYS passed unless --allow-inherit-labels is given
explicitly. That is the whole point of this script: a caller who wants the
default epic-only-label leak protection never has to type the flag, and a
caller who deliberately wants label inheritance must say so loudly with a
distinct, differently-named flag rather than by omission.

Options:
  --parent <id>             Docket (or other parent) bead id (required)
  --title <text>            Packet title (required)
  --body-file <file>        Packet content file, passed as bd create's
                             --body-file (required)
  --acceptance <text>       Acceptance criteria text
  --acceptance-file <file>  Acceptance criteria, read from this file
                             (exactly one of --acceptance/--acceptance-file
                             is required)
  --metadata <json>         Passed through to bd create --metadata
  --label <labels>          Passed through to bd create --labels (comma
                             separated); rarely needed since packets
                             normally carry no explicit labels of their own
  --allow-inherit-labels     Explicit opt-out: omit --no-inherit-labels and
                             let the packet inherit the parent's labels
  -h, --help                 Show this help message
HELP
}

die() {
  echo "create-packet.sh: error: $1" >&2
  exit "${2:-1}"
}

PARENT=""
TITLE=""
BODY_FILE=""
ACCEPTANCE=""
ACCEPTANCE_FILE=""
METADATA=""
LABELS=""
ALLOW_INHERIT=0

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --parent)
    [[ $# -ge 2 ]] || die "--parent requires a value" 2
    PARENT="$2"
    shift 2
    ;;
  --title)
    [[ $# -ge 2 ]] || die "--title requires a value" 2
    TITLE="$2"
    shift 2
    ;;
  --body-file)
    [[ $# -ge 2 ]] || die "--body-file requires a value" 2
    BODY_FILE="$2"
    shift 2
    ;;
  --acceptance)
    [[ $# -ge 2 ]] || die "--acceptance requires a value" 2
    ACCEPTANCE="$2"
    shift 2
    ;;
  --acceptance-file)
    [[ $# -ge 2 ]] || die "--acceptance-file requires a value" 2
    ACCEPTANCE_FILE="$2"
    shift 2
    ;;
  --metadata)
    [[ $# -ge 2 ]] || die "--metadata requires a value" 2
    METADATA="$2"
    shift 2
    ;;
  --label)
    [[ $# -ge 2 ]] || die "--label requires a value" 2
    LABELS="$2"
    shift 2
    ;;
  --allow-inherit-labels)
    ALLOW_INHERIT=1
    shift
    ;;
  *)
    die "unknown option: $1" 2
    ;;
  esac
done

[[ -n $PARENT ]] || die "--parent is required" 2
[[ -n $TITLE ]] || die "--title is required" 2
[[ -n $BODY_FILE ]] || die "--body-file is required" 2
[[ -f $BODY_FILE ]] || die "--body-file not found: $BODY_FILE" 2

if [[ -n $ACCEPTANCE && -n $ACCEPTANCE_FILE ]]; then
  die "pass exactly one of --acceptance / --acceptance-file, not both" 2
fi
if [[ -z $ACCEPTANCE && -z $ACCEPTANCE_FILE ]]; then
  die "one of --acceptance / --acceptance-file is required" 2
fi
if [[ -n $ACCEPTANCE_FILE ]]; then
  [[ -f $ACCEPTANCE_FILE ]] || die "--acceptance-file not found: $ACCEPTANCE_FILE" 2
  ACCEPTANCE="$(cat "$ACCEPTANCE_FILE")"
fi

command -v bd >/dev/null 2>&1 || die "bd not found on PATH"

ARGS=(create "$TITLE" -t task --parent "$PARENT" --body-file "$BODY_FILE" --acceptance "$ACCEPTANCE" --silent)

if [[ $ALLOW_INHERIT -eq 0 ]]; then
  ARGS+=(--no-inherit-labels)
fi
[[ -n $METADATA ]] && ARGS+=(--metadata "$METADATA")
[[ -n $LABELS ]] && ARGS+=(--labels "$LABELS")

NEW_ID="$(bd "${ARGS[@]}")" || die "bd create failed"
[[ -n $NEW_ID ]] || die "bd create returned no id"

bd defer "$NEW_ID" >/dev/null || die "bd defer $NEW_ID failed (packet $NEW_ID was created but is NOT held)"

echo "$NEW_ID"
