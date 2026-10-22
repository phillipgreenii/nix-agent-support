# shellcheck shell=bash
# pg-rescue-fix-ci - the argv of the pg-router fix-ci-script command role
# (docket pg2-q6a96, design D6 and D9). For ONE fix-ci item it claims the item,
# runs `pg-rescue --chain fix-ci -- pg-rescue-fix-ci step ...` (this same tool,
# by its own resolved absolute path), and on every path releases the claim. The
# step requests ONE rerun of the failed CI jobs; when that rerun is spent and CI
# is still failing, the step fails and the chain's give-up handler (which sets
# the marker) hands the item to the worker.
#
# Generic by design: names no employer and no tracker; every machine-specific
# value is an argument. This tool does no git work, takes no repository
# directory and never closes an item (a rerun is asynchronous: the decider
# closes the item when CI turns green). The role executor discards stdout and
# stderr, so the record of a run is <state-dir>/<run-id>.outcome plus
# pg-rescue's own --result-file.

show_help() {
  cat <<'HELP'
pg-rescue-fix-ci: Request one CI rerun for a fix-ci item; hand it to the worker when the rerun is spent

Usage: pg-rescue-fix-ci --item-id ID --repo REPO --pr-number N --tracker-dir DIR
                        [--ci-backend INSTANCE] [--state-dir DIR]
       pg-rescue-fix-ci step <the same options>

For the item ID the tool, in order:

  1. claims the item (bd update ID --claim, actor pg-rescue/<run-id>);
  2. runs  pg-rescue --chain fix-ci -C <state-dir> ... -- pg-rescue-fix-ci step ...
     with PG_RESCUE_ITEM_ID=ID, BEADS_DIR=<tracker-dir>/.beads and
     BEADS_ACTOR=pg-rescue/<run-id> exported;
  3. on pg-rescue exit 0 or 75 (the chain gave up; its give-up handler set the
     marker) exits 0; a passed-through 9 becomes 1; any other code is unchanged;
  4. on every path releases the claim (the item stays open; it is never closed).

The step subcommand decides by the item's own data (bd show ID --json, the
metadata keys failing_builds and ci_rerun_of, both JSON arrays whose elements
are opaque and compared as sets):

  - no ci_rerun_of recorded: runs  pg-connector ci rerun-failed REPO#N
    [--backend INSTANCE], records failing_builds unchanged in ci_rerun_of and
    comments; exit 0. A failed rerun call records nothing and exits 1;
  - ci_rerun_of recorded and every failing build is in it (CI has not reported
    again): does nothing, exit 0;
  - ci_rerun_of recorded and a failing build is not in it (the rerun failed
    too): exit 1, so the chain's give-up hands the item to the worker.

A claim that fails because a peer holds the item is not an error: nothing is
changed and the exit is 0. The marker label is set by the chain's give-up
handler, never by this tool. pg-rescue reads its own config (the fix-ci chain)
from its usual lookup.

Options (every option except --ci-backend and --state-dir is required):
  --item-id ID           The work item to handle
  --repo REPO            The repository (owner/name), as pg-connector names it
  --pr-number N          The pull request number
  --tracker-dir DIR      Absolute directory that contains the tracker's .beads
                         directory
  --ci-backend INSTANCE  The pg-connector ci backend instance, passed to
                         ci rerun-failed as --backend (omit it while exactly one
                         ci backend instance is registered)
  --state-dir DIR        Absolute directory for the outcome record, the
                         pg-rescue result file and pg-rescue's working directory
                         (default: ${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue-fix-ci)
  -h, --help             Show this help message
  -v, --version          Show version information

Exit codes:
  0  the rerun was requested or is awaited; or the chain gave up (marker set);
     or a peer holds the item
  1  any other failure (a pg-rescue exit 9 is remapped to 1)
  2  usage error (checked before bd is touched)

Each run writes "<run-id> <outcome> <item id>" to <state-dir>/<run-id>.outcome
(outcome: ok, gave-up, peer-held or failed) and passes
--result-file <run-id>.result.json in the same directory to pg-rescue.

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

# Bounds for every external step (seconds); there is no watchdog.
BD_TIMEOUT=60
CONNECTOR_TIMEOUT=120
RESCUE_TIMEOUT=900

# usage_error MSG: one diagnostic line, exit 2 (nothing has touched bd yet).
usage_error() {
  echo "pg-rescue-fix-ci: $1 (see --help)" >&2
  exit 2
}

# need_value OPT COUNT: an option must carry a value.
need_value() { [[ $2 -ge 2 ]] || usage_error "option $1 needs a value"; }

mode=run
if [[ ${1:-} == step ]]; then
  mode=step
  shift
fi

# The options exactly as received: the step gets them verbatim after `step`.
orig_args=("$@")

item_id="" repo="" pr_number="" tracker_dir="" ci_backend="" state_dir=""
ci_backend_set=0 state_dir_set=0

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --item-id)
    need_value "$1" $#
    item_id=$2
    shift
    ;;
  --repo)
    need_value "$1" $#
    repo=$2
    shift
    ;;
  --pr-number)
    need_value "$1" $#
    pr_number=$2
    shift
    ;;
  --tracker-dir)
    need_value "$1" $#
    tracker_dir=$2
    shift
    ;;
  --ci-backend)
    need_value "$1" $#
    ci_backend=$2
    ci_backend_set=1
    shift
    ;;
  --state-dir)
    need_value "$1" $#
    state_dir=$2
    state_dir_set=1
    shift
    ;;
  *) usage_error "unexpected argument: $1" ;;
  esac
  shift
done

# Validate everything before bd is touched.
[[ -n $item_id ]] || usage_error "--item-id is required"
[[ $item_id != -* ]] || usage_error "--item-id must not start with '-': $item_id"
[[ -n $repo ]] || usage_error "--repo is required"
[[ $repo != -* && $repo != *[[:space:]]* && $repo != *#* ]] || usage_error "--repo must not start with '-' or contain whitespace or '#': $repo"
[[ -n $pr_number ]] || usage_error "--pr-number is required"
[[ $pr_number =~ ^[1-9][0-9]*$ ]] || usage_error "--pr-number must be a positive integer: $pr_number"
[[ -n $tracker_dir ]] || usage_error "--tracker-dir is required"
[[ $tracker_dir == /* ]] || usage_error "--tracker-dir must be an absolute path: $tracker_dir"
[[ -d $tracker_dir/.beads ]] || usage_error "--tracker-dir has no .beads directory: $tracker_dir"
[[ $ci_backend_set -eq 0 || (-n $ci_backend && $ci_backend != -*) ]] || usage_error "--ci-backend must be a non-empty instance name not starting with '-'"
[[ $state_dir_set -eq 0 || $state_dir == /* ]] || usage_error "--state-dir must be an absolute path: $state_dir"
[[ -n $state_dir ]] || state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue-fix-ci"

# Every bd call, and every handler pg-rescue starts, runs against the tracker.
export BEADS_DIR="$tracker_dir/.beads"

# ---------------------------------------------------------------------------
# step: the command pg-rescue runs. Succeeds when it issued the one rerun or is
# waiting for it; fails when the rerun is spent and CI is still failing.
# ---------------------------------------------------------------------------
if [[ $mode == step ]]; then
  step_actor="${BEADS_ACTOR:-pg-rescue/step-$$}"

  step_fail() {
    echo "pg-rescue-fix-ci step: $1" >&2
    exit 1
  }

  show_json=$(timeout "$BD_TIMEOUT" bd show "$item_id" --json 2>/dev/null) ||
    step_fail "cannot read $item_id"

  # An array is itself; a string is parsed as JSON text (bd stores
  # --set-metadata values as strings); absent is empty; anything else is one
  # opaque element.
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  item_view=$(jq -c '
    def norm:
      if type == "array" then .
      elif type == "string" then ((try fromjson catch .) | if type == "array" then . else [.] end)
      elif . == null then []
      else [.] end;
    (if type == "object" and has("data") then .data else . end)
    | (if type == "array" then .[0] else . end)
    | (.metadata // {}) as $m
    | { recorded: ($m | has("ci_rerun_of")),
        failing: ($m.failing_builds | norm),
        rerun_of: ($m.ci_rerun_of | norm) }
    | . + { fresh: ([.failing[] as $f | select(.rerun_of | any(. == $f) | not) | $f] | length) }
  ' <<<"$show_json") || step_fail "cannot parse the item JSON of $item_id"

  recorded=$(jq -r '.recorded' <<<"$item_view")
  if [[ $recorded == true ]]; then
    fresh=$(jq -r '.fresh' <<<"$item_view")
    if [[ $fresh -eq 0 ]]; then
      echo "pg-rescue-fix-ci step: rerun already requested for $item_id; waiting for CI" >&2
      exit 0
    fi
    step_fail "rerun already requested for $item_id and a build still fails that the rerun did not cover; giving up"
  fi

  # No rerun recorded: request exactly one.
  failing=$(jq -c '.failing' <<<"$item_view")
  connector_args=(ci rerun-failed "$repo#$pr_number")
  if [[ -n $ci_backend ]]; then
    connector_args+=(--backend "$ci_backend")
  fi
  timeout "$CONNECTOR_TIMEOUT" pg-connector "${connector_args[@]}" >&2 ||
    step_fail "pg-connector ci rerun-failed $repo#$pr_number failed"
  timeout "$BD_TIMEOUT" bd update "$item_id" --set-metadata "ci_rerun_of=$failing" --actor "$step_actor" >/dev/null ||
    step_fail "requested the rerun but could not record ci_rerun_of on $item_id"
  timeout "$BD_TIMEOUT" bd comment "$item_id" "pg-rescue-fix-ci: requested one rerun of the failed CI jobs for $repo#$pr_number (failing builds $failing); waiting for CI to report again" --actor "$step_actor" >/dev/null ||
    echo "pg-rescue-fix-ci step: could not comment on $item_id (the rerun is recorded)" >&2
  exit 0
fi

# ---------------------------------------------------------------------------
# run: claim, pg-rescue chain, release.
# ---------------------------------------------------------------------------

# This tool's own resolved absolute path: pg-rescue runs the step through it,
# never through a bare name.
self=$0
[[ $self == */* ]] || self=$(command -v -- "$0") || usage_error "cannot resolve the path of this tool from $0"
self=$(realpath -- "$self") || usage_error "cannot resolve the path of this tool from $0"

run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
actor="pg-rescue/$run_id"
mkdir -p "$state_dir" || {
  echo "pg-rescue-fix-ci: cannot create state directory $state_dir" >&2
  exit 1
}

export BEADS_ACTOR="$actor"
export PG_RESCUE_ITEM_ID="$item_id"

claimed=0 outcome=""

# release_claim: ONE call, so no window leaves a stranded claim.
# shellcheck disable=SC2329  # called only from cleanup, which runs as the EXIT trap
release_claim() {
  timeout "$BD_TIMEOUT" bd update "$item_id" --status open --assignee "" --actor "$actor" >/dev/null
}

# cleanup: runs on every exit path. A claim still held is released (a claim is
# never left held) and the outcome record is written.
# shellcheck disable=SC2329  # invoked through the EXIT trap
cleanup() {
  local rc=$?
  trap - EXIT
  if [[ $claimed -eq 1 ]]; then
    if ! release_claim; then
      echo "pg-rescue-fix-ci: could not release the claim on $item_id" >&2
      rc=1
      outcome=failed
    fi
  fi
  if [[ $rc -ne 0 && $outcome != failed ]]; then
    outcome=failed
  fi
  printf '%s %s %s\n' "$run_id" "${outcome:-failed}" "$item_id" >"$state_dir/$run_id.outcome" ||
    echo "pg-rescue-fix-ci: could not write $state_dir/$run_id.outcome" >&2
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# fail MSG: one diagnostic line, outcome failed, exit 1.
fail() {
  echo "pg-rescue-fix-ci: $1" >&2
  outcome=failed
  exit 1
}

# 1. Claim. A claim lost to a peer is not an error and changes nothing.
claim_err=""
if ! claim_err=$(timeout "$BD_TIMEOUT" bd update "$item_id" --claim --actor "$actor" 2>&1 >/dev/null); then
  show_json=""
  show_json=$(timeout "$BD_TIMEOUT" bd show "$item_id" --json 2>/dev/null) || show_json=""
  assignee=""
  if [[ -n $show_json ]]; then
    assignee=$(jq -r '(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end) | .assignee // ""' <<<"$show_json" 2>/dev/null) || assignee=""
  fi
  if [[ -n $assignee && $assignee != "$actor" ]]; then
    echo "pg-rescue-fix-ci: $item_id is held by $assignee; leaving it alone" >&2
    outcome=peer-held
    exit 0
  fi
  fail "cannot claim $item_id: ${claim_err:-no output}"
fi
claimed=1

# 2. The step under pg-rescue. -C is mandatory: a command role sets no working
# directory. The step is a decision, not a repair, so the verify is a no-op (the
# default would re-run the step).
context="fix-ci repo=$repo pr=$pr_number item=$item_id"
rescue_rc=0
timeout "$RESCUE_TIMEOUT" pg-rescue --chain fix-ci -C "$state_dir" \
  --context "$context" --verify true \
  --result-file "$state_dir/$run_id.result.json" \
  -- "$self" step "${orig_args[@]}" >&2 || rescue_rc=$?

case $rescue_rc in
0)
  outcome=ok
  exit 0
  ;;
75)
  # The chain gave up and its handler set the marker; the claim is released by
  # cleanup.
  outcome=gave-up
  exit 0
  ;;
9) fail "pg-rescue exited 9 (busy); remapped to 1" ;;
*)
  echo "pg-rescue-fix-ci: pg-rescue exited $rescue_rc" >&2
  outcome=failed
  exit "$rescue_rc"
  ;;
esac
