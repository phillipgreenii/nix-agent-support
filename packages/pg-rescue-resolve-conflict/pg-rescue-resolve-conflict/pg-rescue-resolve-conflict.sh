# shellcheck shell=bash
# pg-rescue-resolve-conflict - the argv of the pg-router resolve-conflict-script
# command role (docket pg2-q6a96, design D4 and D9). For ONE resolve-conflict
# item it claims the item, rebases the PR branch in its own linked worktree
# under `pg-rescue --chain conflict`, pushes through pg-rescue-push-branch (the
# only push this tool makes; operator ruling Phillip, 2026-10-22), closes the
# item, and on every path removes the worktree and releases or closes the claim.
#
# Generic by design: names no employer and no tracker; every machine-specific
# value (repo-to-directory map, worktree root, tracker directory) is an
# argument. The role executor discards stdout and stderr, so the record of a run
# is <state-dir>/<run-id>.outcome plus pg-rescue's own --result-file.

show_help() {
  cat <<'HELP'
pg-rescue-resolve-conflict: Resolve one resolve-conflict item by rebasing its PR branch under pg-rescue

Usage: pg-rescue-resolve-conflict --item-id ID --repo REPO --branch BRANCH
                                  --base BASE --head-sha SHA
                                  --repo-dir-map NAME=DIR [--repo-dir-map ...]
                                  --worktree-root DIR --tracker-dir DIR

For the item ID the tool, in order:

  1. claims the item (bd update ID --claim, actor pg-rescue/<run-id>);
  2. fetches BRANCH and BASE from origin in the repository directory the map
     names for REPO and adds a detached linked worktree of origin/BRANCH under
     the worktree root;
  3. runs  pg-rescue --chain conflict -C <worktree> ... -- git rebase origin/BASE
     with PG_RESCUE_ITEM_ID=ID, BEADS_DIR=<tracker-dir>/.beads and
     BEADS_ACTOR=pg-rescue/<run-id> exported (the verify command checks that no
     rebase is in progress and that origin/BASE is an ancestor of HEAD);
  4. on pg-rescue exit 0, pushes with pg-rescue-push-branch --expected-sha SHA
     and closes the item, naming the run id in the reason;
  5. on pg-rescue exit 75 (the chain gave up; its give-up handler set the
     marker) pushes nothing and releases the claim;
  6. on any other pg-rescue exit releases the claim and exits 1;
  7. on every path removes the worktree and releases or closes the claim.

A claim that fails because a peer holds the item is not an error: nothing is
changed and the exit is 0. The marker label is set by the chain's give-up
handler, never by this tool.

Options (every option is required):
  --item-id ID           The work item to resolve
  --repo REPO            The repository; its last path component is the key
                         into the repo-dir map (REPO acme/widget -> widget)
  --branch BRANCH        The PR branch to rebase and push
  --base BASE            The base branch to rebase onto (never pushed)
  --head-sha SHA         The PR branch head the item recorded (40 hex); the
                         push lease value (--expected-sha)
  --repo-dir-map N=DIR   Maps a repository name to its local clone (repeatable)
  --worktree-root DIR    Absolute directory under which worktrees are created
  --tracker-dir DIR      Absolute directory that contains the tracker's .beads
                         directory
  -h, --help             Show this help message
  -v, --version          Show version information

Exit codes:
  0  the item was resolved; or the chain gave up (marker set); or the push
     lease was rejected; or a peer holds the item
  1  any other failure (a pg-rescue exit 9 is remapped to 1)
  2  usage error (checked before bd is touched)

Each run writes "<run-id> <outcome> <item id>" to
${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue-resolve-conflict/<run-id>.outcome
(outcome: resolved, gave-up, lease-rejected, peer-held or failed) and passes
--result-file <run-id>.result.json in the same directory to pg-rescue.

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

# Bounds for every external step (seconds); there is no watchdog.
BD_TIMEOUT=60
GIT_LOCAL_TIMEOUT=60
GIT_REMOTE_TIMEOUT=120
PUSH_TIMEOUT=120
RESCUE_TIMEOUT=1800

# usage_error MSG: one diagnostic line, exit 2 (nothing has touched bd yet).
usage_error() {
  echo "pg-rescue-resolve-conflict: $1 (see --help)" >&2
  exit 2
}

item_id="" repo="" branch="" base="" head_sha="" worktree_root="" tracker_dir=""
declare -A repo_dirs=()

# need_value OPT COUNT: an option must carry a value.
need_value() { [[ $2 -ge 2 ]] || usage_error "option $1 needs a value"; }

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
  --branch)
    need_value "$1" $#
    branch=$2
    shift
    ;;
  --base)
    need_value "$1" $#
    base=$2
    shift
    ;;
  --head-sha)
    need_value "$1" $#
    head_sha=$2
    shift
    ;;
  --repo-dir-map)
    need_value "$1" $#
    [[ $2 == ?*=?* ]] || usage_error "--repo-dir-map needs NAME=DIR, got: $2"
    map_name=${2%%=*}
    [[ -z ${repo_dirs[$map_name]+x} ]] || usage_error "--repo-dir-map names $map_name more than once"
    repo_dirs[$map_name]=${2#*=}
    shift
    ;;
  --worktree-root)
    need_value "$1" $#
    worktree_root=$2
    shift
    ;;
  --tracker-dir)
    need_value "$1" $#
    tracker_dir=$2
    shift
    ;;
  *) usage_error "unexpected argument: $1" ;;
  esac
  shift
done

# Validate everything, including the repo-dir lookup, before bd is touched.
[[ -n $item_id ]] || usage_error "--item-id is required"
[[ $item_id != -* ]] || usage_error "--item-id must not start with '-': $item_id"
[[ -n $repo ]] || usage_error "--repo is required"
[[ -n $branch ]] || usage_error "--branch is required"
[[ $branch != -* ]] || usage_error "--branch must not start with '-': $branch"
[[ -n $base ]] || usage_error "--base is required"
[[ $base != -* ]] || usage_error "--base must not start with '-': $base"
[[ $branch != "$base" ]] || usage_error "--branch equals --base ($base); the base branch is never pushed"
[[ -n $head_sha ]] || usage_error "--head-sha is required"
[[ $head_sha =~ ^[0-9a-fA-F]{40}$ ]] || usage_error "--head-sha must be a full 40-character hexadecimal object name"
[[ -n $worktree_root ]] || usage_error "--worktree-root is required"
[[ $worktree_root == /* ]] || usage_error "--worktree-root must be an absolute path: $worktree_root"
[[ -n $tracker_dir ]] || usage_error "--tracker-dir is required"
[[ $tracker_dir == /* ]] || usage_error "--tracker-dir must be an absolute path: $tracker_dir"
[[ -d $tracker_dir/.beads ]] || usage_error "--tracker-dir has no .beads directory: $tracker_dir"

repo_name=${repo##*/}
[[ -n $repo_name ]] || usage_error "--repo has no last path component: $repo"
[[ -n ${repo_dirs[$repo_name]+x} ]] || usage_error "no --repo-dir-map entry for repository $repo_name"
repo_dir=${repo_dirs[$repo_name]}
[[ -d $repo_dir ]] || usage_error "--repo-dir-map directory for $repo_name does not exist: $repo_dir"

run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
actor="pg-rescue/$run_id"
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue-resolve-conflict"
mkdir -p "$state_dir" || {
  echo "pg-rescue-resolve-conflict: cannot create state directory $state_dir" >&2
  exit 1
}

# Every bd call, and every handler pg-rescue starts, runs against the tracker
# and as the run's actor (handlers inherit the wrapper's environment).
export BEADS_DIR="$tracker_dir/.beads"
export BEADS_ACTOR="$actor"
export PG_RESCUE_ITEM_ID="$item_id"
# The verify command is a fixed string; the base branch reaches it by variable.
export PG_RESCUE_RESOLVE_BASE="$base"

claimed=0 closed=0 outcome="" wt=""

# remove_worktree: unlink the linked worktree, whatever state a failed rebase
# left it in.
# shellcheck disable=SC2329  # called only from cleanup, which runs as the EXIT trap
remove_worktree() {
  if ! timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree remove --force "$wt" >/dev/null 2>&1; then
    rm -rf "$wt"
    timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree prune >/dev/null 2>&1 || true
  fi
}

# release_claim: ONE call, so no window leaves a stranded claim.
# shellcheck disable=SC2329  # called only from cleanup, which runs as the EXIT trap
release_claim() {
  timeout "$BD_TIMEOUT" bd update "$item_id" --status open --assignee "" --actor "$actor" >/dev/null
}

# cleanup: runs on every exit path. The worktree is removed, a claim still held
# is released (a claim is never left held), and the outcome record is written.
# shellcheck disable=SC2329  # invoked through the EXIT trap
cleanup() {
  local rc=$?
  trap - EXIT
  if [[ -n $wt && -e $wt ]]; then
    remove_worktree
  fi
  if [[ $claimed -eq 1 && $closed -eq 0 ]]; then
    if ! release_claim; then
      echo "pg-rescue-resolve-conflict: could not release the claim on $item_id" >&2
      rc=1
      outcome=failed
    fi
  fi
  if [[ $rc -ne 0 && $outcome != failed ]]; then
    outcome=failed
  fi
  printf '%s %s %s\n' "$run_id" "${outcome:-failed}" "$item_id" >"$state_dir/$run_id.outcome" ||
    echo "pg-rescue-resolve-conflict: could not write $state_dir/$run_id.outcome" >&2
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# fail MSG: one diagnostic line, outcome failed, exit 1.
fail() {
  echo "pg-rescue-resolve-conflict: $1" >&2
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
    echo "pg-rescue-resolve-conflict: $item_id is held by $assignee; leaving it alone" >&2
    outcome=peer-held
    exit 0
  fi
  fail "cannot claim $item_id: ${claim_err:-no output}"
fi
claimed=1

# 2. Isolated worktree of the PR branch (detached checkout of origin/BRANCH).
mkdir -p "$worktree_root" || fail "cannot create --worktree-root $worktree_root"
wt="$worktree_root/$repo_name-$run_id"
timeout "$GIT_REMOTE_TIMEOUT" git -C "$repo_dir" fetch --quiet origin \
  "+refs/heads/$branch:refs/remotes/origin/$branch" \
  "+refs/heads/$base:refs/remotes/origin/$base" ||
  fail "fetch of $branch and $base from origin failed in $repo_dir"
timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree add --quiet --detach "$wt" "refs/remotes/origin/$branch" ||
  fail "cannot add a worktree for origin/$branch under $worktree_root"

# 3. Rebase under pg-rescue. -C is mandatory: a command role sets no working
# directory. The default verify re-runs the (non-idempotent) rebase, so an
# explicit one checks the state instead.
# shellcheck disable=SC2016  # single quotes are deliberate: the verify string expands in `sh -c`, not here
verify='d=$(git rev-parse --git-path rebase-merge) && a=$(git rev-parse --git-path rebase-apply) && [ ! -e "$d" ] && [ ! -e "$a" ] && git merge-base --is-ancestor "origin/$PG_RESCUE_RESOLVE_BASE" HEAD'
context="resolve-conflict repo=$repo branch=$branch item=$item_id"
rescue_rc=0
timeout "$RESCUE_TIMEOUT" pg-rescue --chain conflict -C "$wt" \
  --context "$context" --verify "$verify" \
  --result-file "$state_dir/$run_id.result.json" \
  -- git rebase "origin/$base" >&2 || rescue_rc=$?

case $rescue_rc in
0) ;;
75)
  # The chain gave up and its handler set the marker; the claim is released by
  # cleanup, nothing is pushed.
  outcome=gave-up
  exit 0
  ;;
9) fail "pg-rescue exited 9 (busy); remapped to 1" ;;
*) fail "pg-rescue exited $rescue_rc" ;;
esac

# 4. Push (the only push this tool makes), then close.
push_rc=0
timeout "$PUSH_TIMEOUT" pg-rescue-push-branch --worktree "$wt" --branch "$branch" \
  --base "$base" --expected-sha "$head_sha" >&2 || push_rc=$?

case $push_rc in
0) ;;
3)
  echo "pg-rescue-resolve-conflict: push lease rejected for $branch (remote moved); not retried" >&2
  outcome=lease-rejected
  exit 0
  ;;
*) fail "pg-rescue-push-branch exited $push_rc" ;;
esac

timeout "$BD_TIMEOUT" bd close "$item_id" --actor "$actor" \
  --reason "pg-rescue run $run_id: resolved (rebased onto origin/$base and pushed)" >/dev/null ||
  fail "pushed $branch but could not close $item_id"
closed=1
outcome=resolved
exit 0
