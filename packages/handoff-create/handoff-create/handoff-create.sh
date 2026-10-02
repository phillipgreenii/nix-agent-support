# shellcheck shell=bash
# handoff-create - create a handoff bead correctly (bead pg2-2xfbi). The
# mechanical details (type, priority, title prefix, first body line, metadata,
# the human label policy, the task fallback, the read-back) live here so a
# model neither gets them wrong nor forgets them. The contract for what a
# handoff bead IS lives in the beads-lifecycle:handoff-bead skill; this script
# only implements its "Creating a handoff" section.
#
# nix build already sources handoff-create.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash handoff-create.sh` run (e.g. local bats), where nothing has sourced it.
if ! declare -F hc_bd >/dev/null 2>&1; then
  # shellcheck disable=SC1091  # sibling file resolved at runtime, by design
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/handoff-create.bash"
fi

die() {
  echo "handoff-create: $1" >&2
  exit "${2:-2}"
}

show_help() {
  cat <<'HELP'
handoff-create: Create a handoff bead correctly (type, P0, title, body, metadata, labels)

Usage: handoff-create (--attended | --unattended) --session-id ID --title TEXT
                      --body-file FILE [--label L]... [--actor ID] [--bd-dir DIR]

Creates a P0 bead of type `handoff` whose title is `Handoff: TEXT`, whose body
starts `Handoff from session ID` followed by the contents of FILE, and whose
metadata carries {"handed_off_from_session":"ID"}. It then reads the bead back
(`bd show`) and verifies what landed.

Required:
  --attended | --unattended
      No default: the caller MUST decide.
        --attended    a human is in the session and asked for or approved the
                      handoff. The bead is labelled `human`, so no drain agent
                      picks it up before the operator starts the next session.
        --unattended  no human is present (out of context, an auto-trigger run,
                      any agent-initiated handoff). The bead gets NO `human`
                      label, so a drain agent MAY pick it up.
  --session-id ID   The source session id (this run's real, observed id).
  --title TEXT      The subject. A leading `Handoff:` is not doubled.
  --body-file FILE  The carry-over body (pointers to beads and labels, and a
                    first step). The `Handoff from session ID` line is added
                    for you; do not write it.

Options:
  --label L         Extra label (repeatable), for example auto-session-wrapped.
                    `human` is controlled by --attended/--unattended, so
                    --label human with --unattended is refused.
  --actor ID        Actor for the audit trail. Default: the session id.
  --bd-dir DIR      Tracker root to run bd against (bd -C DIR). Default: the
                    caller's current directory.
  -h, --help        Show this help message
  -v, --version     Show version information

If the database does not register type `handoff` (bd says `invalid issue type:
handoff`), the create is retried ONCE as type `task` with the same title, body,
metadata and labels. Any other create failure is reported and not retried.

Output: the new bead id on stdout; what the read-back verified on stderr.

Exit status:
  0  created and verified
  1  unexpected error (for example bd or jq is not installed)
  2  usage error; nothing was created
  3  bd create failed; nothing was created
  4  created, but the read-back disagrees (the id is named on stderr)

Examples:
  handoff-create --attended --session-id "$SID" --title "finish pg-router retry" --body-file /tmp/body.md
  handoff-create --unattended --session-id "$SID" --title "finish pg-router retry" \
    --body-file /tmp/body.md --label auto-session-wrapped

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

MODE=""
SESSION_ID=""
TITLE_TEXT=""
BODY_FILE=""
ACTOR=""
HC_BD_DIR=""
LABELS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --attended | --unattended)
    [[ -z $MODE || $MODE == "${1#--}" ]] || die "--attended and --unattended are mutually exclusive"
    MODE="${1#--}"
    shift
    ;;
  --session-id | --title | --body-file | --label | --actor | --bd-dir)
    [[ $# -ge 2 && -n $2 ]] || die "$1 requires a value"
    case "$1" in
    --session-id) SESSION_ID="$2" ;;
    --title) TITLE_TEXT="$2" ;;
    --body-file) BODY_FILE="$2" ;;
    --label) LABELS+=("$2") ;;
    --actor) ACTOR="$2" ;;
    --bd-dir) HC_BD_DIR="$2" ;;
    esac
    shift 2
    ;;
  *)
    die "unknown argument: $1 (see --help)"
    ;;
  esac
done

[[ -n $MODE ]] || die "one of --attended or --unattended is required (no default: decide whether a human is in this session)"
[[ -n $SESSION_ID ]] || die "--session-id is required"
[[ -n $TITLE_TEXT ]] || die "--title is required"
[[ -n $BODY_FILE ]] || die "--body-file is required"
[[ $SESSION_ID =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "--session-id must match [A-Za-z0-9._-]+ (got '$SESSION_ID')"
[[ -f $BODY_FILE && -r $BODY_FILE ]] || die "--body-file is not a readable file: $BODY_FILE"
[[ -s $BODY_FILE ]] || die "--body-file is empty: $BODY_FILE"
if [[ -n $HC_BD_DIR && ! -d $HC_BD_DIR ]]; then
  die "--bd-dir is not a directory: $HC_BD_DIR"
fi
[[ -n $ACTOR ]] || ACTOR="$SESSION_ID"

ATTENDED=0
[[ $MODE == attended ]] && ATTENDED=1

# The final label set: `human` iff attended, then the passthrough labels
# (de-duplicated). `human` passed by hand under --unattended is refused rather
# than silently dropped, so the caller learns the policy.
ALL_LABELS=()
[[ $ATTENDED -eq 1 ]] && ALL_LABELS+=(human)
for label in ${LABELS[@]+"${LABELS[@]}"}; do
  [[ $label =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]*$ ]] || die "invalid --label '$label' (letters, digits, and . _ : / - only)"
  if [[ $label == human ]]; then
    [[ $ATTENDED -eq 1 ]] || die "--label human conflicts with --unattended: an unattended handoff MUST NOT carry human"
    continue
  fi
  dup=0
  for have in ${ALL_LABELS[@]+"${ALL_LABELS[@]}"}; do
    [[ $have == "$label" ]] && dup=1
  done
  [[ $dup -eq 1 ]] || ALL_LABELS+=("$label")
done

TITLE="$(hc_normalize_title "$TITLE_TEXT")" || die "--title is empty after removing the Handoff: prefix"
META="$(hc_build_metadata "$SESSION_ID")"
DESC="$(
  printf 'Handoff from session %s\n\n' "$SESSION_ID"
  cat "$BODY_FILE"
)"

command -v bd >/dev/null 2>&1 || die "bd is not on PATH" 1
command -v jq >/dev/null 2>&1 || die "jq is not on PATH" 1

TYPE=handoff
if hc_create_once "$TYPE" "$TITLE" "$DESC" "$META" "$ACTOR" ${ALL_LABELS[@]+"${ALL_LABELS[@]}"}; then
  create_rc=0
else
  create_rc=$?
fi

if [[ $create_rc -ne 0 ]] && hc_is_unregistered_type_failure; then
  echo "handoff-create: type handoff is not registered in this database; retrying ONCE as type task" >&2
  TYPE=task
  if hc_create_once "$TYPE" "$TITLE" "$DESC" "$META" "$ACTOR" ${ALL_LABELS[@]+"${ALL_LABELS[@]}"}; then
    create_rc=0
  else
    create_rc=$?
  fi
fi

if [[ $create_rc -ne 0 ]]; then
  echo "handoff-create: bd create (type $TYPE) failed with exit $create_rc; nothing was created and it was not retried:" >&2
  [[ -z $HC_ERR ]] || printf '%s\n' "$HC_ERR" >&2
  [[ -z $HC_OUT ]] || printf '%s\n' "$HC_OUT" >&2
  exit 3
fi

NEW_ID="$(hc_parse_id "$HC_OUT")" || die "bd create succeeded but its output is not a bead id: '$HC_OUT'" 4

if hc_readback "$NEW_ID" "$TYPE" "$TITLE" "$SESSION_ID" "$ATTENDED" ${ALL_LABELS[@]+"${ALL_LABELS[@]}"}; then
  readback_rc=0
else
  readback_rc=$?
fi
if [[ $readback_rc -ne 0 ]]; then
  echo "handoff-create: bead $NEW_ID WAS created but the read-back disagrees; inspect it with: bd show $NEW_ID" >&2
  exit 4
fi

printf '%s\n' "$NEW_ID"
