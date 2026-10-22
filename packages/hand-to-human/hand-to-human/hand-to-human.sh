# shellcheck shell=bash
# hand-to-human - hand a bead to the operator (bead pg2-ikkpk). Writes the
# strict label pair human + attention and removes escalated in ONE update call,
# so no agent hand-writes the labels and no bead is ever left with neither the
# escalated nor the human labels. The vocabulary lives in
# docs/guides/bead-label-vocabulary.md (nix-agent-support) and ADR 0105; the
# lifecycle rule is beads-lifecycle LV-1..LV-6.
#
# nix build already sources hand-to-human.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash hand-to-human.sh` run (e.g. local bats), where nothing has sourced it.
if ! declare -F hth_bd >/dev/null 2>&1; then
  # shellcheck disable=SC1091  # sibling file resolved at runtime, by design
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/hand-to-human.bash"
fi

die() {
  echo "hand-to-human: $1" >&2
  exit "${2:-2}"
}

show_help() {
  cat <<'HELP'
hand-to-human: Hand a bead to the operator (human + attention, escalated removed, one update)

Usage: hand-to-human ID [--backend NAME | --bd-dir DIR] [--also-remove LABEL]...
                        [--actor ID]

Adds the labels `human` and `attention` to bead ID and removes `escalated`, all
in ONE update call (the adds are in the same call as the removal, so the bead
never carries neither the escalated nor the human labels). Use it whenever an
agent or system hands a bead to the operator because automation could not
resolve it. Do NOT use it for planned operator work (verify, checkpoint,
decision and handoff beads), which carries `human` only.

By default the tool runs `bd update` and then reads the bead back (`bd show`),
checking that both labels are present and every removed label is absent.

Options:
  --backend NAME      Reach the tracker through `pg-connector issue update
                      --backend NAME` instead of bd (the escalation-triager has
                      no direct bd access to its tracker). Not read back.
  --bd-dir DIR        Tracker root to run bd against (bd -C DIR). Default: the
                      caller's current directory. Not valid with --backend.
  --also-remove L     Remove one more label in the same call (repeatable), for
                      example needs-split-review. `human` and `attention`
                      cannot be removed.
  --actor ID          Actor for the audit trail (bd only; default: bd's own
                      resolution from BEADS_ACTOR).
  -h, --help          Show this help message
  -v, --version       Show version information

Output: the bead id on stdout; what the read-back verified on stderr.

Exit status:
  0  updated and verified
  1  unexpected error (for example bd or jq is not installed)
  2  usage error; nothing was written
  3  the update call failed; nothing was written
  4  updated, but the read-back disagrees (the id is named on stderr)

Examples:
  hand-to-human pg2-abc12 --actor "$SID"
  hand-to-human pg2-abc12 --also-remove needs-split-review
  hand-to-human pg2-abc12 --backend pg-connector-issue-beads-pg2

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

BEAD_ID=""
BACKEND=""
ACTOR=""
HTH_BD_DIR=""
EXTRA_REMOVE=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --backend | --bd-dir | --also-remove | --actor)
    [[ $# -ge 2 && -n $2 ]] || die "$1 requires a value"
    case "$1" in
    --backend) BACKEND="$2" ;;
    --bd-dir) HTH_BD_DIR="$2" ;;
    --also-remove) EXTRA_REMOVE+=("$2") ;;
    --actor) ACTOR="$2" ;;
    esac
    shift 2
    ;;
  -*)
    die "unknown argument: $1 (see --help)"
    ;;
  *)
    [[ -z $BEAD_ID ]] || die "exactly one bead id is expected (got '$BEAD_ID' and '$1')"
    BEAD_ID="$1"
    shift
    ;;
  esac
done

[[ -n $BEAD_ID ]] || die "a bead id is required"
[[ $BEAD_ID =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "invalid bead id '$BEAD_ID'"
if [[ -n $BACKEND ]]; then
  [[ -z $HTH_BD_DIR ]] || die "--bd-dir and --backend are mutually exclusive"
  [[ -z $ACTOR ]] || die "--actor is not supported with --backend (pg-connector carries no actor flag)"
  [[ $BACKEND =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "invalid --backend '$BACKEND'"
fi
if [[ -n $HTH_BD_DIR && ! -d $HTH_BD_DIR ]]; then
  die "--bd-dir is not a directory: $HTH_BD_DIR"
fi
for label in ${EXTRA_REMOVE[@]+"${EXTRA_REMOVE[@]}"}; do
  [[ $label =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]*$ ]] || die "invalid --also-remove '$label' (letters, digits, and . _ : / - only)"
  if [[ $label == "$HTH_HUMAN_LABEL" || $label == "$HTH_ATTENTION_LABEL" ]]; then
    die "--also-remove $label is refused: the call adds $HTH_HUMAN_LABEL and $HTH_ATTENTION_LABEL"
  fi
done

REMOVE=()
mapfile -t REMOVE < <(hth_remove_labels ${EXTRA_REMOVE[@]+"${EXTRA_REMOVE[@]}"})

if [[ -n $BACKEND ]]; then
  command -v pg-connector >/dev/null 2>&1 || die "pg-connector is not on PATH" 1
  if hth_run_connector "$BACKEND" "$BEAD_ID" "${REMOVE[@]}"; then
    rc=0
  else
    rc=$?
  fi
  if [[ $rc -ne 0 ]]; then
    echo "hand-to-human: pg-connector issue update failed with exit $rc; nothing was written and it was not retried:" >&2
    [[ -z $HTH_ERR ]] || printf '%s\n' "$HTH_ERR" >&2
    [[ -z $HTH_OUT ]] || printf '%s\n' "$HTH_OUT" >&2
    exit 3
  fi
  printf '%s\n' "$BEAD_ID"
  exit 0
fi

command -v bd >/dev/null 2>&1 || die "bd is not on PATH" 1
command -v jq >/dev/null 2>&1 || die "jq is not on PATH" 1

if hth_run_bd "$BEAD_ID" "$ACTOR" "${REMOVE[@]}"; then
  rc=0
else
  rc=$?
fi
if [[ $rc -ne 0 ]]; then
  echo "hand-to-human: bd update failed with exit $rc; nothing was written and it was not retried:" >&2
  [[ -z $HTH_ERR ]] || printf '%s\n' "$HTH_ERR" >&2
  [[ -z $HTH_OUT ]] || printf '%s\n' "$HTH_OUT" >&2
  exit 3
fi

if hth_readback "$BEAD_ID" "${REMOVE[@]}"; then
  readback_rc=0
else
  readback_rc=$?
fi
if [[ $readback_rc -ne 0 ]]; then
  echo "hand-to-human: bead $BEAD_ID WAS updated but the read-back disagrees; inspect it with: bd show $BEAD_ID" >&2
  exit 4
fi

printf '%s\n' "$BEAD_ID"
