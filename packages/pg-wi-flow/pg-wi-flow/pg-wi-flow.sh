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

This packet (tc-9ddu3.1.1) implements the read/reservation core: query,
list, next, claim, release. Later packets in the tc-9ddu3.1 docket add
context/annotate/round/advance/create-child/merge/close/escalate/resolve
as subcommands of this same entry point.

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
*)
  echo "pg-wi-flow: unknown command: $command" >&2
  show_help >&2
  exit 1
  ;;
esac
