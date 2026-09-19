# shellcheck shell=bash

# nix build already sources pg-wi-flow.bash (and, transitively, the three
# lib/ libraries it guards-sources) ahead of this body (mkBashScript's
# hasSupportBash injection); this guard only fires for a raw `bash
# pg-wi-flow.sh` run (e.g. local bats, or an interactive checkout run)
# where nothing has sourced it yet.
if ! declare -F pgwf_cmd_query >/dev/null 2>&1; then
  # shellcheck disable=SC1091 # sibling file, resolved at source time
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-wi-flow.bash"
fi

show_help() {
  cat <<'HELP'
pg-wi-flow: bead-workflow CLI framework

Usage: pg-wi-flow COMMAND [ARGS...]

Packet tc-9ddu3.1.1 implements the read/reservation core: query, list,
next, claim, release. This packet (tc-9ddu3.1.3) adds the item-content and
stage-transition write verbs: annotate, record-verdict, round, advance,
create-child, merge, close, close-duplicate. context/--render lands in a
separate packet (tc-9ddu3.1.2); escalate/resolve in tc-9ddu3.1.4.

Commands:
  query [--stage S]... [--attended]
      Print the fully built bd filter set for the requested query shape,
      one flag/value per line.

  list [--stage S]... [--attended] [--questions] [--unpooled]
       [--stale --days N | --stale --reserved-hours H]
      Print (as a JSON array) the items the query would admit, or one of
      the special --unpooled/--stale views.

  next [--stage S]...
      Reserve the next claimable item (leaf, or a container's descent per
      the state model), printing "ID STAGE WORKFLOW" or "none".

  claim ID
      Transfer a reservation to the caller's identity, printing
      "ID STAGE WORKFLOW".

  release ID
      Release ID, clearing the assignee in the same call as the status
      change.

  annotate ID [--kind K] [--component C] [--premise P] [--acceptance T]
           [--append-description T] [--append-notes T] [--design T]
      The only way to write item content. Flags are combinable in one
      call.

  record-verdict ID --concern C --json <file|->
      Run by a reviewer leaf; records C's verdict for the current round in
      item metadata. Stdin allowed via "-".

  round ID
      Reads every verdict recorded for the current round, merges them,
      increments the round counter, and prints the merged verdict (ready,
      gaps, blocked, "duplicate ID", or "related IDs") plus
      WI_MUST_ESCALATE=true when the counter reaches iteration_bound
      without ready.

  advance ID --to STAGE [--reason TEXT]
      Validates the target stage exists in ID's workflow (a move to a
      lower-order stage requires --reason), swaps the stage label, and
      adds the container label if ID has any children. Never creates a
      land bead.

  create-child PARENT --title T [--kind K] [--stage S]
               [--blocked-by ID]... [--description T]
      New child at the workflow's entry stage (or --stage, validated
      against the child's inherited workflow). PARENT gains the container
      label in the same call and stays open. Each --blocked-by adds a
      blocking edge from the child to ID (repeatable).

  merge ID... --into SURVIVOR
      Closes each listed ID as a duplicate of SURVIVOR with related
      links; SURVIVOR gets a note listing the merged symptoms.

  close ID --reason TEXT [--trace "BULLET=DISPOSITION"]...
      Closes ID. With --trace, refuses unless every bullet parsed from
      ID's description has a disposition (an existing id, a plain label,
      or "filed:TITLE" to file a new entry-stage item).

  close-duplicate ID --of OF
      Refuses unless ID is newer than OF and OF is open on a fresh read;
      adds a related link.

Global options (before COMMAND):
  --actor NAME   Explicit actor override. Accepted ONLY outside Claude
                 Code (refused when PG_WI_FLOW_IDENT is already set --
                 inside Claude Code the actor always composes from
                 PG_WI_FLOW_IDENT plus the item's stage).
  -h, --help     Show this help message
  -v, --version  Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

case "${1:-}" in
-h | --help)
  show_help
  exit 0
  ;;
esac

while [[ $# -gt 0 ]]; do
  case "$1" in
  --actor)
    if [[ -n ${PG_WI_FLOW_IDENT:-} ]]; then
      echo "pg-wi-flow: --actor is not accepted inside Claude Code (PG_WI_FLOW_IDENT is set); the actor composes from the environment" >&2
      exit 1
    fi
    # shellcheck disable=SC2034 # consumed by pg-wi-flow.bash's pgwf_current_actor
    PGWF_EXPLICIT_ACTOR="$2"
    shift 2
    ;;
  --)
    shift
    break
    ;;
  -*)
    echo "pg-wi-flow: unknown option: $1" >&2
    exit 1
    ;;
  *)
    break
    ;;
  esac
done

if [[ $# -lt 1 ]]; then
  echo "pg-wi-flow: missing COMMAND" >&2
  show_help >&2
  exit 1
fi

command="$1"
shift

case "$command" in
query)
  pgwf_cmd_query "$@"
  ;;
list)
  pgwf_cmd_list "$@"
  ;;
next)
  pgwf_cmd_next "$@"
  ;;
claim)
  pgwf_cmd_claim "$@"
  ;;
release)
  pgwf_cmd_release "$@"
  ;;
annotate)
  pgwf_cmd_annotate "$@"
  ;;
record-verdict)
  pgwf_cmd_record_verdict "$@"
  ;;
round)
  pgwf_cmd_round "$@"
  ;;
advance)
  pgwf_cmd_advance "$@"
  ;;
create-child)
  pgwf_cmd_create_child "$@"
  ;;
merge)
  pgwf_cmd_merge "$@"
  ;;
close)
  pgwf_cmd_close "$@"
  ;;
close-duplicate)
  pgwf_cmd_close_duplicate "$@"
  ;;
*)
  echo "pg-wi-flow: unknown command: $command" >&2
  show_help >&2
  exit 1
  ;;
esac
