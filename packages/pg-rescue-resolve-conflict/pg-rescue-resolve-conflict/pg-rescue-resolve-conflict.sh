# shellcheck shell=bash
# pg-rescue-resolve-conflict - the argv of the pg-router resolve-conflict-script
# command role (docket pg2-q6a96, design D4 and D9). For ONE resolve-conflict
# item it claims the item, rebases the PR branch in its own linked worktree
# under `pg-rescue --chain conflict`, pushes through pg-rescue-push-branch (the
# only push this tool makes; operator ruling Phillip, 2026-10-22), closes the
# item, and on every path removes the worktree and releases or closes the claim.
# Revision 2 (design D4, D5, D9): a stale head or a rejected push lease closes
# the item, handlers cannot push, PUSH_AGENT_RESOLVED gates a push of an
# agent-resolved rebase, stranded claims and worktrees are swept, repeated
# failures are capped, and the shim keeps its own log.
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

  1. sweeps claims stranded by a killed earlier run, claims the item (bd update
     ID --claim, actor pg-rescue/<run-id>), and, when the item already failed
     SCRIPT_MAX_FAILURES times, runs only `pg-rescue --chain give-up` (the
     marker is set, nothing else runs) and exits 0;
  2. fetches BRANCH and BASE from origin in the repository directory the map
     names for REPO; when origin/BRANCH is not SHA the item describes a head
     that no longer exists, so it is closed as stale (exit 0) and nothing else
     happens; otherwise adds a detached linked worktree of origin/BRANCH under
     the worktree root;
  3. runs  pg-rescue --chain conflict -C <worktree> ... -- git rebase origin/BASE
     with PG_ROUTER_ITEM_ID=ID, BEADS_DIR=<tracker-dir>/.beads and
     BEADS_ACTOR=pg-rescue/<run-id> exported, and with a git configuration
     environment that makes the push URL of origin unusable (a handler cannot
     publish by itself);
  4. on pg-rescue exit 0 pushes with pg-rescue-push-branch --expected-sha SHA
     (a push lease pinned to SHA) and closes the item, naming the run id in the
     reason; a rejected push lease closes the item as stale (exit 0);
  5. on pg-rescue exit 75 (the chain gave up; its give-up handler set the
     marker) pushes nothing and releases the claim;
  6. on any other pg-rescue exit counts a failure (script_failures on the
     item), releases the claim and exits 1;
  7. on every path removes the worktree and releases or closes the claim.

A claim that fails because a peer holds the item, or because the item is
already closed, is not an error: nothing is changed and the exit is 0. The
marker label is set by the chain's give-up handler, never by this tool.

Options (every option is required):
  --item-id ID           The work item to resolve
  --repo REPO            The repository; its last path component is the key
                         into the repo-dir map (REPO acme/widget -> widget)
  --branch BRANCH        The PR branch to rebase and push
  --base BASE            The base branch to rebase onto (never pushed)
  --head-sha SHA         The PR branch head the item recorded (40 hex); the
                         stale-head comparison and the push lease value
  --repo-dir-map N=DIR   Maps a repository name to its local clone (repeatable)
  --worktree-root DIR    Absolute directory under which worktrees are created
  --tracker-dir DIR      Absolute directory that contains the tracker's .beads
                         directory
  -h, --help             Show this help message
  -v, --version          Show version information

Environment (each overrides the default of the same name in the script):
  PUSH_AGENT_RESOLVED    true (default): push whatever pg-rescue resolved. false:
                         push only a rebase no agent handler resolved; an
                         agent-resolved rebase goes to the give-up chain
                         instead (nothing pushed, claim released, outcome
                         gave-up)
  SCRIPT_MAX_FAILURES    Failed runs after which an item goes straight to the
                         give-up chain (default 3)
  SWEEP_AGE              Seconds after which a recorded claim of an earlier run
                         counts as stranded and is released (default 7200)

Exit codes:
  0  the item was resolved; or the chain gave up (marker set); or the item was
     closed as stale (its head moved, or the push lease was rejected); or a peer
     holds the item or it is already closed
  1  any other failure (a pg-rescue exit 9 is remapped to 1)
  2  usage error (checked before bd is touched)

Each run writes "<run-id> <outcome> <item id>" to
${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue-resolve-conflict/<run-id>.outcome
(outcome: resolved, gave-up, stale, gone, peer-held or failed), passes
--result-file <run-id>.result.json in the same directory to pg-rescue, and
appends its own stderr to <run-id>.log there (a role executor discards both
streams, so the log is the record of a failed run).

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

# Bounds for every external step (seconds); there is no watchdog.
BD_TIMEOUT=60
GIT_LOCAL_TIMEOUT=60
GIT_REMOTE_TIMEOUT=120
PUSH_TIMEOUT=120
RESCUE_TIMEOUT=1800

# Policy switches and sweep bounds; an environment variable of the same name
# overrides each (a test ages a claim or lowers the cap that way).
# PUSH_AGENT_RESOLVED (design D5, operator ruling Phillip, 2026-10-22): true
# pushes whatever pg-rescue resolved; false pushes only a rebase no agent
# handler resolved and sends an agent-resolved one to the give-up chain.
PUSH_AGENT_RESOLVED=${PUSH_AGENT_RESOLVED:-true}
SCRIPT_MAX_FAILURES=${SCRIPT_MAX_FAILURES:-3}
SWEEP_AGE=${SWEEP_AGE:-7200}

# The push URL pg-rescue's processes see for origin (design D5): unusable, so a
# handler that runs `git push origin` fails. Set through the environment of that
# one process tree; repository configuration is never written.
PUSH_DENIED_URL=/nonexistent/pg-rescue-push-denied-for-handlers

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

[[ $PUSH_AGENT_RESOLVED == true || $PUSH_AGENT_RESOLVED == false ]] ||
  usage_error "PUSH_AGENT_RESOLVED must be true or false, got: $PUSH_AGENT_RESOLVED"
[[ $SCRIPT_MAX_FAILURES =~ ^[0-9]+$ ]] || usage_error "SCRIPT_MAX_FAILURES must be a whole number, got: $SCRIPT_MAX_FAILURES"
[[ $SWEEP_AGE =~ ^[0-9]+$ ]] || usage_error "SWEEP_AGE must be a whole number of seconds, got: $SWEEP_AGE"

run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
actor="pg-rescue/$run_id"
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue-resolve-conflict"
mkdir -p "$state_dir" || {
  echo "pg-rescue-resolve-conflict: cannot create state directory $state_dir" >&2
  exit 1
}
# The role executor discards stdout and stderr, so the run keeps its own
# stderr (the shim's diagnostics and every step's) in <run-id>.log.
log_file="$state_dir/$run_id.log"
claim_file="$state_dir/$run_id.claim"
result_file="$state_dir/$run_id.result.json"
{ : >>"$log_file"; } 2>/dev/null || {
  echo "pg-rescue-resolve-conflict: cannot write $log_file" >&2
  exit 1
}
exec 2>>"$log_file"

# Every bd call, and every handler pg-rescue starts, runs against the tracker
# and as the run's actor (handlers inherit the wrapper's environment).
export BEADS_DIR="$tracker_dir/.beads"
export BEADS_ACTOR="$actor"
export PG_ROUTER_ITEM_ID="$item_id"
# The verify command is a fixed string; the base branch reaches it by variable.
export PG_RESCUE_RESOLVE_BASE="$base"

claimed=0 closed=0 outcome="" wt="" child_pid="" failures_before=0

# warn MSG: one diagnostic line on stderr (the run log); never fails the run.
warn() { echo "pg-rescue-resolve-conflict: $1" >&2; }

# remove_worktree: unlink the linked worktree, whatever state a failed rebase
# left it in.
# shellcheck disable=SC2329  # called only from cleanup, which runs as the EXIT trap
remove_worktree() {
  if ! timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree remove --force "$wt" >/dev/null 2>&1; then
    rm -rf "$wt"
    timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree prune >/dev/null 2>&1 || true
  fi
}

# release_claim: ONE call, so no window leaves a stranded claim. A failed run
# carries its counter in the same call (design D9).
# shellcheck disable=SC2329  # called only from cleanup, which runs as the EXIT trap
release_claim() {
  local -a counter=()
  if [[ $outcome == failed ]]; then
    counter=(--set-metadata "script_failures=$((failures_before + 1))")
  fi
  timeout "$BD_TIMEOUT" bd update "$item_id" --status open --assignee "" --actor "$actor" "${counter[@]}" >/dev/null
}

# cleanup: runs on every exit path. The worktree is removed, a claim still held
# is released (a claim is never left held), and the outcome record is written.
# shellcheck disable=SC2329  # invoked through the EXIT trap
cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  if [[ $rc -ne 0 && $outcome != failed ]]; then
    outcome=failed
  fi
  if [[ -n $wt && -e $wt ]]; then
    remove_worktree
  fi
  if [[ $claimed -eq 1 && $closed -eq 0 ]]; then
    if release_claim; then
      rm -f "$claim_file"
    else
      echo "pg-rescue-resolve-conflict: could not release the claim on $item_id" >&2
      rc=1
      outcome=failed
    fi
  fi
  printf '%s %s %s\n' "$run_id" "${outcome:-failed}" "$item_id" >"$state_dir/$run_id.outcome" ||
    echo "pg-rescue-resolve-conflict: could not write $state_dir/$run_id.outcome" >&2
  exit "$rc"
}

# on_signal CODE: INT and TERM. Bash defers a trap until its foreground child
# ends, so the long steps run through bg_run; here the running step is stopped
# before the EXIT trap (cleanup) removes the worktree and releases the claim.
# shellcheck disable=SC2329  # invoked through the INT and TERM traps
on_signal() {
  local code=$1 watchdog
  trap '' INT TERM
  if [[ -n $child_pid ]]; then
    kill -TERM "$child_pid" 2>/dev/null || true
    (
      sleep 10
      kill -KILL "$child_pid" 2>/dev/null
    ) >/dev/null 2>&1 &
    watchdog=$!
    wait "$child_pid" 2>/dev/null || true
    kill "$watchdog" 2>/dev/null || true
  fi
  exit "$code"
}

# bg_run CMD...: run an external step as a background job and wait for it, so a
# signal reaches on_signal at once instead of after the step.
bg_run() {
  local rc=0
  "$@" &
  child_pid=$!
  wait "$child_pid" || rc=$?
  child_pid=""
  return "$rc"
}

# The traps are installed before the first bd call (the sweep's).
trap cleanup EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

# fail MSG: one diagnostic line, outcome failed, exit 1.
fail() {
  warn "$1"
  outcome=failed
  exit 1
}

# item_json ID: the normalized issue object (whichever envelope bd emits).
item_json() {
  timeout "$BD_TIMEOUT" bd show "$1" --json --actor "$actor" 2>/dev/null |
    jq -c '(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end)'
}

# file_mtime FILE: modification time in epoch seconds (GNU stat, else BSD).
file_mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"; }

# sweep_stale_claims: a SIGKILL runs no trap, so a claim an earlier run recorded
# in <state-dir>/<run-id>.claim and never released is released here once its
# file is older than SWEEP_AGE and the item still names that actor as assignee;
# the worktree directory of that run id is removed too. A failure is reported
# and never fails the run (design D9).
sweep_stale_claims() {
  local f now mtime stale_item stale_actor stale_run assignee d
  now=$(date +%s)
  for f in "$state_dir"/*.claim; do
    [[ -f $f ]] || continue
    mtime=$(file_mtime "$f") || {
      warn "sweep: cannot read the age of $f"
      continue
    }
    ((now - mtime > SWEEP_AGE)) || continue
    stale_item="" stale_actor=""
    {
      IFS= read -r stale_item || true
      IFS= read -r stale_actor || true
    } <"$f"
    stale_run=$(basename "$f" .claim)
    if [[ -n $stale_item && $stale_item != -* && -n $stale_actor ]]; then
      assignee=$(item_json "$stale_item" | jq -r '.assignee // ""' 2>/dev/null) || assignee=""
      if [[ $assignee == "$stale_actor" ]]; then
        # Released AS the holder: bd refuses to reassign a held item to another actor.
        if timeout "$BD_TIMEOUT" bd update "$stale_item" --status open --assignee "" --actor "$stale_actor" >/dev/null; then
          warn "sweep: released the stranded claim on $stale_item held by $stale_actor"
        else
          warn "sweep: could not release $stale_item held by $stale_actor; leaving $f"
          continue
        fi
      fi
    else
      warn "sweep: $f is malformed; removing it"
    fi
    if [[ $stale_run =~ ^[0-9]{8}T[0-9]{6}Z-[0-9]+$ ]]; then
      for d in "$worktree_root"/*-"$stale_run"; do
        [[ -d $d ]] || continue
        rm -rf "$d" || warn "sweep: could not remove $d"
      done
    fi
    timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree prune >/dev/null 2>&1 || true
    rm -f "$f"
  done
}

# close_item REASON: close the claimed item; the claim ends with the close.
close_item() {
  timeout "$BD_TIMEOUT" bd close "$item_id" --actor "$actor" --reason "$1" >/dev/null ||
    fail "could not close $item_id"
  closed=1
  rm -f "$claim_file"
}

# run_give_up RESULT_FILE CONTEXT: only `pg-rescue --chain give-up`, whose
# handler labels the item and hands it to the worker. Exit 75 (or 0) is the
# expected end; any other exit is a failed run.
run_give_up() {
  local rc=0
  bg_run timeout "$RESCUE_TIMEOUT" pg-rescue --chain give-up -C "$state_dir" \
    --context "$2" --verify true --result-file "$1" -- false >&2 || rc=$?
  case $rc in
  0 | 75) outcome=gave-up ;;
  *) fail "pg-rescue --chain give-up exited $rc" ;;
  esac
}

# agent_resolved RESULT_FILE: true unless pg-rescue's run-log line PROVES the
# rebase was resolved without an agent handler (result success, or a resolved
# attempt none of whose handlers is tagged agent). A missing, unparsable or
# unrecognised file counts as agent-resolved (design D5).
agent_resolved() {
  local kind
  [[ -r $1 ]] || return 0
  kind=$(jq -r '
    (.attempts // []) as $a
    | ([$a[] | select(.outcome == "resolved")]) as $r
    | if .result == "success" then "plain"
      elif ($r | length) > 0 then
        (if any($r[]; ((.tags // []) | index("agent")) != null) then "agent" else "plain" end)
      else "agent" end' "$1" 2>/dev/null) || kind=agent
  [[ $kind != plain ]]
}

sweep_stale_claims || warn "sweep failed; continuing"

# 1. Claim. A claim lost to a peer, or on an already closed item, is not an
# error and changes nothing.
claim_err=""
if ! claim_err=$(timeout "$BD_TIMEOUT" bd update "$item_id" --claim --actor "$actor" 2>&1 >/dev/null); then
  show_json=$(item_json "$item_id") || show_json=""
  item_status="" assignee=""
  if [[ -n $show_json ]]; then
    item_status=$(jq -r '.status // ""' <<<"$show_json" 2>/dev/null) || item_status=""
    assignee=$(jq -r '.assignee // ""' <<<"$show_json" 2>/dev/null) || assignee=""
  fi
  if [[ $item_status == closed ]]; then
    warn "$item_id is already closed; nothing to do"
    outcome=gone
    exit 0
  fi
  if [[ -n $assignee && $assignee != "$actor" ]]; then
    warn "$item_id is held by $assignee; leaving it alone"
    outcome=peer-held
    exit 0
  fi
  fail "cannot claim $item_id: ${claim_err:-no output}"
fi
claimed=1
printf '%s\n%s\n' "$item_id" "$actor" >"$claim_file" || fail "cannot record the claim in $claim_file"

# Failure cap: an item that already failed SCRIPT_MAX_FAILURES times runs no
# rebase and no agent handler; it goes straight to the give-up chain.
failures_raw=$(item_json "$item_id" | jq -r '.metadata.script_failures // 0 | tostring' 2>/dev/null) || failures_raw=0
[[ $failures_raw =~ ^[0-9]+$ ]] || failures_raw=0
failures_before=$((10#$failures_raw))
if ((failures_before >= SCRIPT_MAX_FAILURES)); then
  warn "$item_id already failed $failures_before times (cap $SCRIPT_MAX_FAILURES); running the give-up chain only"
  run_give_up "$result_file" "resolve-conflict repo=$repo branch=$branch item=$item_id failure-cap"
  exit 0
fi

# 2. Fetch, then compare origin/BRANCH with the item's head: a differing head
# means the item describes a head that no longer exists (a new head is a new
# item), so the item is closed as stale before any worktree or handler.
mkdir -p "$worktree_root" || fail "cannot create --worktree-root $worktree_root"
bg_run timeout "$GIT_REMOTE_TIMEOUT" git -C "$repo_dir" fetch --quiet origin \
  "+refs/heads/$branch:refs/remotes/origin/$branch" \
  "+refs/heads/$base:refs/remotes/origin/$base" ||
  fail "fetch of $branch and $base from origin failed in $repo_dir"
remote_sha=$(timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" rev-parse --verify -q "refs/remotes/origin/$branch") ||
  fail "cannot resolve origin/$branch in $repo_dir"
if [[ ${remote_sha,,} != "${head_sha,,}" ]]; then
  warn "origin/$branch is $remote_sha but the item recorded $head_sha; closing $item_id as stale"
  close_item "pg-rescue run $run_id: stale (origin/$branch is $remote_sha, the item recorded $head_sha)"
  outcome=stale
  exit 0
fi

# Isolated worktree of the PR branch (detached checkout of origin/BRANCH). The
# prune first drops the registration of any worktree an earlier run left behind.
wt="$worktree_root/$repo_name-$run_id"
timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree prune >/dev/null 2>&1 || true
timeout "$GIT_LOCAL_TIMEOUT" git -C "$repo_dir" worktree add --quiet --detach "$wt" "refs/remotes/origin/$branch" ||
  fail "cannot add a worktree for origin/$branch under $worktree_root"

# 3. Rebase under pg-rescue. -C is mandatory: a command role sets no working
# directory. The default verify re-runs the (non-idempotent) rebase, so an
# explicit one checks the state instead. Only this process tree gets the git
# configuration environment that makes origin's push URL unusable; the new key
# goes after any inherited GIT_CONFIG_* entries.
# shellcheck disable=SC2016  # single quotes are deliberate: the verify string expands in `sh -c`, not here
verify='d=$(git rev-parse --git-path rebase-merge) && a=$(git rev-parse --git-path rebase-apply) && [ ! -e "$d" ] && [ ! -e "$a" ] && git merge-base --is-ancestor "origin/$PG_RESCUE_RESOLVE_BASE" HEAD'
context="resolve-conflict repo=$repo branch=$branch item=$item_id"
git_cfg_n=${GIT_CONFIG_COUNT:-0}
[[ $git_cfg_n =~ ^[0-9]+$ ]] || git_cfg_n=0
rescue_rc=0
bg_run env "GIT_CONFIG_COUNT=$((git_cfg_n + 1))" \
  "GIT_CONFIG_KEY_$git_cfg_n=remote.origin.pushurl" \
  "GIT_CONFIG_VALUE_$git_cfg_n=$PUSH_DENIED_URL" \
  timeout "$RESCUE_TIMEOUT" pg-rescue --chain conflict -C "$wt" \
  --context "$context" --verify "$verify" \
  --result-file "$result_file" \
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

# With PUSH_AGENT_RESOLVED=false an agent-resolved rebase is not pushed: the
# give-up chain hands the item to the worker instead.
if [[ $PUSH_AGENT_RESOLVED == false ]] && agent_resolved "$result_file"; then
  warn "the rebase was resolved by an agent handler and PUSH_AGENT_RESOLVED is false; running the give-up chain instead of pushing"
  run_give_up "$state_dir/$run_id.give-up.result.json" "$context agent-resolved-not-pushed"
  exit 0
fi

# 4. Push (the only push this tool makes, run WITHOUT the unusable push URL),
# then close. A rejected lease means the item's head is no longer the remote's:
# no retry, no marker, the item is closed as stale.
push_rc=0
bg_run timeout "$PUSH_TIMEOUT" pg-rescue-push-branch --worktree "$wt" --worktree-root "$worktree_root" \
  --branch "$branch" --base "$base" --expected-sha "$head_sha" >&2 || push_rc=$?

case $push_rc in
0) ;;
3)
  warn "push lease rejected for $branch (the remote moved from $head_sha); closing $item_id as stale, not retried"
  close_item "pg-rescue run $run_id: stale (push lease rejected: origin/$branch is no longer $head_sha)"
  outcome=stale
  exit 0
  ;;
*) fail "pg-rescue-push-branch exited $push_rc" ;;
esac

close_item "pg-rescue run $run_id: resolved (rebased onto origin/$base and pushed)"
outcome=resolved
exit 0
