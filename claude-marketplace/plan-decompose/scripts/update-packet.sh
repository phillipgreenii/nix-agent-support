#!/usr/bin/env bash
# update-packet.sh
#
# Rewrites the CONTENT of an already-created plan-decompose work packet: one
# `bd update <id> --body-file <file> [--acceptance <text>] [--actor <id>]`,
# behind the shared acceptance guard (lib/acceptance-guard.bash). It is a
# separate script from create-packet.sh so that the rule "MUST NOT call
# create-packet again for an existing packet" stays literally true, and it has
# a deliberately tiny flag surface: it can never stamp or clear metadata, edit
# labels, create, or defer (speedup plan P5; the curation stamp rule lives in
# plan-decompose/SKILL.md "Curation stamp", reconcile's restamps stay their own
# explicit `bd update --set-metadata` calls).
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/acceptance-guard.bash
source "$SCRIPT_DIR/lib/acceptance-guard.bash"

show_help() {
  cat <<'HELP'
update-packet.sh: rewrite the content of an existing plan-decompose work packet

Usage:
  update-packet.sh --id <packet-id> --body-file <file>
                    [--acceptance-file <file>] [--actor <id>]

Runs, in order:
  the acceptance guard, against --acceptance-file when given, else against the
    packet's STORED acceptance (bd show <id> --json); exit 3, no update, when
    the body repeats the acceptance criteria (an "Acceptance criteria" heading
    followed by a checklist, or 2 or more whole acceptance lines appearing as
    whole lines in the body)
  bd update <id> --body-file <file> [--acceptance "<text>"] [--actor <id>]

Acceptance criteria live ONLY in the acceptance field. Re-curating an old
packet that carries a duplicated block therefore drops the duplicate from the
body.

This script never touches metadata, labels or status: it has no --metadata,
--label or --allow-inherit-labels flag and rejects every flag not listed here.
Restamps (--set-metadata) stay their own explicit bd call.

Options:
  --id <id>                 Packet bead id (required)
  --body-file <file>        New packet content, passed as bd update's
                             --body-file (required)
  --acceptance-file <file>  New acceptance criteria, read from this file; when
                             omitted the stored acceptance is left unchanged
  --actor <id>              Explicit agent actor identity (e.g. the session id,
                             optionally suffixed with a role). Passed to bd
                             update so the audit trail records the agent, not
                             the operator's git user.name.
  -h, --help                 Show this help message

Exit codes: 0 updated; 1 failure; 2 usage; 3 acceptance duplicated in the body.
HELP
}

die() {
  echo "update-packet.sh: error: $1" >&2
  exit "${2:-1}"
}

ID=""
BODY_FILE=""
ACCEPTANCE_FILE=""
ACTOR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --id)
    [[ $# -ge 2 ]] || die "--id requires a value" 2
    ID="$2"
    shift 2
    ;;
  --body-file)
    [[ $# -ge 2 ]] || die "--body-file requires a value" 2
    BODY_FILE="$2"
    shift 2
    ;;
  --acceptance-file)
    [[ $# -ge 2 ]] || die "--acceptance-file requires a value" 2
    ACCEPTANCE_FILE="$2"
    shift 2
    ;;
  --actor)
    [[ $# -ge 2 ]] || die "--actor requires a value" 2
    ACTOR="$2"
    shift 2
    ;;
  *)
    die "unknown option: $1" 2
    ;;
  esac
done

[[ -n $ID ]] || die "--id is required" 2
[[ $ID != -* ]] || die "--id must not start with '-' (it would reach bd update as a flag): $ID" 2
[[ -n $BODY_FILE ]] || die "--body-file is required" 2
[[ -f $BODY_FILE ]] || die "--body-file not found: $BODY_FILE" 2
if [[ -n $ACCEPTANCE_FILE ]]; then
  [[ -f $ACCEPTANCE_FILE ]] || die "--acceptance-file not found: $ACCEPTANCE_FILE" 2
fi

command -v bd >/dev/null 2>&1 || die "bd not found on PATH"
command -v jq >/dev/null 2>&1 || die "jq not found on PATH"

ACCEPTANCE=""
if [[ -n $ACCEPTANCE_FILE ]]; then
  ACCEPTANCE="$(cat "$ACCEPTANCE_FILE")"
  [[ -n ${ACCEPTANCE//[[:space:]]/} ]] ||
    die "--acceptance-file is empty (it would clear the stored acceptance): $ACCEPTANCE_FILE" 2
  GUARD_ACCEPTANCE="$ACCEPTANCE"
else
  # The stored acceptance. bd show --json wraps in an envelope ({"data": ...})
  # in some versions and prints a bare array in others; the key is absent when
  # the acceptance is empty.
  SHOW_JSON="$(bd show "$ID" --json)" || die "bd show $ID failed"
  GUARD_ACCEPTANCE="$(
    jq -r '(if type == "object" and has("data") then .data else . end)
      | (if type == "array" then .[0] else . end)
      | .acceptance_criteria // ""' <<<"$SHOW_JSON"
  )" || die "could not read the stored acceptance of $ID from bd show --json"
fi

rc=0
acceptance_guard_check "$BODY_FILE" "$GUARD_ACCEPTANCE" || rc=$?
case $rc in
0) ;;
3) exit 3 ;;
*) die "acceptance guard could not run (status $rc); refusing to proceed" ;;
esac

ARGS=(update "$ID" --body-file "$BODY_FILE")
[[ -n $ACCEPTANCE_FILE ]] && ARGS+=(--acceptance "$ACCEPTANCE")
[[ -n $ACTOR ]] && ARGS+=(--actor "$ACTOR")

bd "${ARGS[@]}" >/dev/null || die "bd update $ID failed"
echo "$ID"
