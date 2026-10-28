# shellcheck shell=bash
# pg-repo-runner - the facade of the pg-router-managed async runners (bead
# pg2-zj2v7m, F1). This entry point owns argument parsing, the subcommand
# dispatch and the kernel sequence; the steps themselves are the rr_* functions
# in pg-repo-runner.bash. The role bodies of runners B, A and C land in later
# beads: F1 provides the kernel and stubs that exit 2 "not implemented"; F2 (bead
# pg2-j0itft) replaces the `tips` source and the `verify` role (runner B).
#
# nix build already sources pg-repo-runner.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash pg-repo-runner.sh` run (local bats), where nothing sourced it.
if ! declare -F rr_log >/dev/null 2>&1; then
  # shellcheck disable=SC1091  # sibling file resolved at runtime, by design
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-repo-runner.bash"
fi

# The binaries the kernel uses arrive as local variables the nix build injects
# (absolute store paths); a raw run has none, so each falls back to its bare
# name. `export` is what lets shellcheck see these as used by
# pg-repo-runner.bash, which the built script only `source`s from the store.
export RR_GIT_BIN="${RR_GIT_BIN:-git}"
export RR_JQ_BIN="${RR_JQ_BIN:-jq}"
export RR_TQ_BIN="${RR_TQ_BIN:-tq}"
export RR_NICE_BIN="${RR_NICE_BIN:-nice}"
# PG_REPO_RUNNER_NIX_BIN replaces the nix binary (a test seam, like the load
# average one: the bats suites use it to count and classify nix runs).
export RR_NIX_BIN="${PG_REPO_RUNNER_NIX_BIN:-${RR_NIX_BIN:-}}"
export RR_NIX_LOG_WRAPPED_BIN="${RR_NIX_LOG_WRAPPED_BIN:-}"

# Exit status of a "not now" answer: pg-router re-offers the item.
RR_EXIT_BUSY=9

show_help() {
  cat <<'HELP'
pg-repo-runner: Kernel and facade of the pg-router-managed async repo runners

Usage: pg-repo-runner COMMAND [OPTIONS]

Commands:
  tips             Source: emit one JSONL item per allow-listed repo (verify on)
                   whose primary tip has no check verdict
  workspace-tips   Source: emit the debounced workspace tip tuple (not
                   implemented yet; exits 2)
  verify           Role: check the repo tip named by PG_ROUTER_ITEM_ID
                   (<repo>@<sha>) with an immutable-snapshot nix flake check
  sync             Role: sync the repo named by PG_ROUTER_ITEM_ID (same)
  build            Role: build the whole workspace (same)
  status           Print the verdict store, the lease and the kill switch

Options:
  -h, --help       Show this help message
  -v, --version    Show version information

The roles run the kernel steps in this order, and stop at the first that fails:

  1. resolve   PG_ROUTER_ITEM_ID is "<key>" or "<key>@<sha>"; <key> MUST be in
               the config allow-list (repos.<key>) AND a repo key of
               <workspace_root>/pn-workspace.toml. Otherwise exit 2.
  2. path      The real path of <workspace_root>/<key> MUST equal
               <real workspace_root>/<key> and be the top level of a git
               repository (a symlink, a plain directory or a path outside the
               workspace is refused). Otherwise exit 2. (verify and sync only.)
  3. enabled   repos.<key>.verify (for verify) or repos.<key>.sync (for sync)
               MUST be true; if not, the run is skipped and exits 0.
  4. kill      A file <state dir>/push.disabled, or the file named by the
               config key kill_switch, exists: exit 9.
  5. load      The 1-minute load average MUST be strictly below the threshold
               (thresholds.max_load, else the core count): otherwise exit 9.
  6. lease     One lease file in the state dir, shared by every role, taken
               without waiting. Held by a live process: exit 9. A dead owner (or
               a recycled pid) is reclaimed.
  7. act       The role body (verify is implemented; sync and build are stubs
               that exit 2 in this version).

verify, in its act step: <sha> MUST exist (else the run is skipped, exit 0); if
the repo's primary branch no longer points at <sha> a "superseded" verdict is
stored and no nix runs; otherwise it runs

  nice -n <level> pg-nix-log-wrapped nix flake check --no-write-lock-file \
      "git+file://<canonical path>?rev=<sha>"

(an immutable commit: no working tree is read) and stores a check verdict:
green on exit 0, red on any other non-zero exit, with the measured duration and
the sampled 1-minute load in the record. A signal exit (128 + n) is a
malfunction: exit 1, no verdict, pg-router retries. A verdict that already
exists for <sha> ends the run at once (exit 0, no nix). verify needs a sha.

Exit codes:
  0  the run completed, whatever the verdict (a red check exits 0)
  9  busy: not now (kill switch, load, lease held); pg-router re-offers the item
  2  usage error, refusal, or a role that is not implemented yet
  1  malfunction (unreadable config, no load reading, state directory unusable)

status [--json] [--repo KEY]:
  Prints every stored verdict (optionally one repo's), whether the lease is free,
  held or stale, and whether a kill file is present. --json prints only the
  verdicts, as one JSON array. It reads the state directory and nothing else.

Files and environment:
  $XDG_CONFIG_HOME/pg-repo-runner/config.toml   the config the home module renders
                                                (override: PG_REPO_RUNNER_CONFIG)
  $XDG_STATE_HOME/pg-repo-runner/               state dir: run.log, lease,
                                                verdicts/<repo>/<sha>.<kind>.json
  PG_ROUTER_ITEM_ID    the item pg-router offers; the roles read the repo from it
  PG_REPO_RUNNER_LOADAVG   replaces the 1-minute load reading (test seam)
  PG_REPO_RUNNER_MAX_LOAD  replaces the load threshold
  PG_REPO_RUNNER_NCPU      replaces the core count used as the default threshold
  PG_REPO_RUNNER_NIX_BIN   replaces the nix binary (test seam)
  PG_REPO_RUNNER_RETRY_WINDOW   seconds an emitted tip item stays offered
                                (else thresholds.retry_window, else 3600)

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  rr_err "$1"
  rr_log "exit=${2:-1} $1"
  exit "${2:-1}"
}

# decline MESSAGE: the "not now" answer. The lease, if any, is released by the
# EXIT trap; pg-router re-offers the item until it expires.
decline() {
  rr_err "busy: $1"
  rr_log "exit=$RR_EXIT_BUSY busy: $1"
  exit "$RR_EXIT_BUSY"
}

# --- role bodies (sync and build are stubs; F5 and F8 replace them) ---------

# early_verify: before the admission steps. A verdict that is already stored for
# the item's sha means the work is done (Idempotent Receiver): the run ends with
# exit 0 and starts no nix, whatever the load or the lease. Returns 0 to end the
# run, 1 to carry on.
# shellcheck disable=SC2329  # invoked indirectly as "early_$role"
early_verify() {
  if rr_verdict_exists "$RR_ITEM_REPO" "$RR_ITEM_SHA" check; then
    rr_log "exit=0 skipped: a check verdict for $RR_ITEM_REPO@$RR_ITEM_SHA is already stored"
    return 0
  fi
  return 1
}

# act_verify: runner B. The precheck is only that the sha exists; the canonical
# working tree is never read (the check runs on an immutable git snapshot).
# shellcheck disable=SC2329  # invoked indirectly as "act_$role"
act_verify() {
  local repo="$RR_ITEM_REPO" root branch full tip url log rc t0 t1 started finished load0 load1 extra now
  root="$(rr_cfg '.workspace_root // empty')"

  # precheck: the sha exists as a commit
  if ! full="$(rr_git -C "$RR_REPO_PATH" rev-parse --verify -q "${RR_ITEM_SHA}^{commit}")"; then
    rr_log "skipped: $RR_ITEM_SHA is not a commit of $repo"
    return 0
  fi
  # the full sha is the verdict key (an abbreviated item sha resolves here)
  if rr_verdict_exists "$repo" "$full" check; then
    rr_log "skipped: a check verdict for $repo@$full is already stored"
    return 0
  fi

  # latest wins: only the current tip of the primary branch is checked
  branch="$(rr_repo_branch "$root" "$repo")" || {
    rr_err "cannot read the primary branch of '$repo' from pn-workspace.toml"
    return 1
  }
  tip="$(rr_tip_of "$RR_REPO_PATH" "$branch")" || {
    rr_err "the primary branch refs/heads/$branch of '$repo' does not exist"
    return 1
  }
  if [[ $tip != "$full" ]]; then
    now="$(rr_now)"
    rr_verdict_write "$repo" "$full" check superseded locked "$now" "$now" 0 "" || return 1
    rr_log "superseded: the tip of $branch is $tip, not $full; no nix run"
    return 0
  fi

  [[ -n ${RR_NIX_BIN:-} ]] || {
    rr_err "no nix binary is configured (RR_NIX_BIN)"
    return 1
  }
  log="$RR_STATE_DIR/logs/$repo/$full.check.log"
  mkdir -p "$RR_STATE_DIR/logs/$repo" || return 1
  url="$(rr_snapshot_url "$RR_REPO_PATH" "$full")"

  started="$(rr_now)"
  t0="$(rr_epoch)"
  load0="$(rr_loadavg1)" || load0=""
  rr_log "check start: nix flake check --no-write-lock-file $url (load $load0)"
  rc=0
  rr_nix_run flake check --no-write-lock-file "$url" >"$log" 2>&1 || rc=$?
  t1="$(rr_epoch)"
  finished="$(rr_now)"
  load1="$(rr_loadavg1)" || load1=""
  rr_log "check end: exit=$rc after $((t1 - t0))s (load $load1), output in $log"

  if rr_signal_exit "$rc"; then
    rr_err "nix was killed by signal $((rc - 128)) (exit $rc): a malfunction, no verdict is stored"
    return 1
  fi

  # The duration and the load are the calibration input for thresholds.max_load:
  # the 1-minute load average sampled when the check started and when it ended.
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  extra="$(rr_jq -nc --argjson d "$((t1 - t0))" --arg l0 "$load0" --arg l1 "$load1" \
    '{durationS: $d, loadStart: ($l0 | tonumber? // null), loadEnd: ($l1 | tonumber? // null)}')" || return 1
  if ((rc == 0)); then
    rr_verdict_write "$repo" "$full" check green locked "$started" "$finished" "$rc" "$log" "$extra" || return 1
  else
    rr_verdict_write "$repo" "$full" check red locked "$started" "$finished" "$rc" "$log" "$extra" || return 1
  fi
  return 0
}
# shellcheck disable=SC2329  # invoked indirectly as "act_$role"
act_sync() {
  rr_err "role 'sync' is not implemented yet"
  return 2
}
# shellcheck disable=SC2329  # invoked indirectly as "act_$role"
act_build() {
  rr_err "role 'build' is not implemented yet"
  return 2
}

# --- the kernel sequence ----------------------------------------------------

# run_role ROLE SCOPE: SCOPE is `repo` (the item names a repo) or `workspace`.
run_role() {
  local role="$1" scope="$2" root item key rc kill load max
  # shellcheck disable=SC2034  # read by rr_log in pg-repo-runner.bash
  RR_ROLE="$role"
  rr_init_state || die "cannot create the state directory" 1
  rr_load_config || die "cannot load the config" 1
  root="$(rr_cfg '.workspace_root // empty')"
  [[ -n $root && -d $root ]] || die "config workspace_root is missing or not a directory" 1

  if [[ $scope == repo ]]; then
    # 1. resolve
    item="${PG_ROUTER_ITEM_ID:-}"
    [[ -n $item ]] || die "PG_ROUTER_ITEM_ID is not set" 2
    rr_parse_item_id "$item" || die "malformed PG_ROUTER_ITEM_ID: $item" 2
    key="$RR_ITEM_REPO"
    if [[ $role == verify && -z $RR_ITEM_SHA ]]; then
      die "verify needs an item id <repo>@<sha>, got: $item" 2
    fi
    rr_repo_listed "$key" || die "repo '$key' is not in the allow-list" 2
    rc=0
    rr_workspace_has_key "$root" "$key" || rc=$?
    case "$rc" in
    0) ;;
    1) die "repo '$key' is not a repo key of pn-workspace.toml" 2 ;;
    *) die "cannot read $root/pn-workspace.toml" 1 ;;
    esac
    # 2. path
    rr_path_check "$root" "$key" || die "path check failed: $root/$key is not the top level of a git repository at that exact real path" 2
    # 3. enabled
    if ! rr_repo_flag "$key" "$role"; then
      rr_log "exit=0 skipped: $role is not enabled for '$key'"
      exit 0
    fi
    # 3b. idempotent receiver: a role may end the run when its work is stored
    if declare -F "early_$role" >/dev/null && "early_$role"; then
      exit 0
    fi
  fi

  # 4. kill switch
  if kill="$(rr_kill_switch_active)"; then
    decline "kill switch present: $kill"
  fi
  # 5. load
  load="$(rr_loadavg1)" || die "cannot read the 1-minute load average" 1
  max="$(rr_max_load)" || die "cannot determine the load threshold" 1
  rr_load_admits "$load" "$max" || decline "1-minute load $load is not below $max"
  # 6. lease
  rc=0
  rr_lease_acquire || rc=$?
  case "$rc" in
  0) ;;
  1) decline "the lease is held by another run" ;;
  *) die "cannot use the state directory $RR_STATE_DIR" 1 ;;
  esac
  trap 'rr_lease_release' EXIT

  # 7. act
  rr_log "admitted"
  rc=0
  "act_$role" || rc=$?
  rr_log "exit=$rc"
  exit "$rc"
}

# --- tips (the source of runner B) -----------------------------------------

# run_tips: one JSONL record per allow-listed repo (verify on) whose primary tip
# has no check verdict, on stdout and nothing else. The id carries the sha, so a
# re-emit is the same event and a new tip is a new one; expiresAt is the retry
# window. "Tips without a verdict" is derived state, so a crash resumes by
# construction. A repo that fails the resolve or the path check is logged and
# skipped: one broken entry never blanks the others. Not subject to the kill
# switch, the load check or the lease: it only reads refs and the store.
run_tips() {
  local root key now_e at expires branch tip rc keys
  [[ $# -eq 0 ]] || die "tips takes no arguments" 2
  # shellcheck disable=SC2034  # read by rr_log in pg-repo-runner.bash
  RR_ROLE=tips
  rr_init_state || die "cannot create the state directory" 1
  rr_load_config || die "cannot load the config" 1
  root="$(rr_cfg '.workspace_root // empty')"
  [[ -n $root && -d $root ]] || die "config workspace_root is missing or not a directory" 1
  now_e="$(rr_epoch)"
  at="$(rr_iso_at "$now_e")"
  expires="$(rr_iso_at "$((now_e + $(rr_retry_window)))")"
  keys="$(rr_cfg '(.repos // {}) | to_entries | map(select(.value.verify == true) | .key) | sort | .[]')" ||
    die "cannot read the allow-list" 1
  while IFS= read -r key; do
    [[ -n $key ]] || continue
    if ! rr_valid_key "$key"; then
      rr_log "tips: skipped '$key': not a plain repo key"
      continue
    fi
    rc=0
    rr_workspace_has_key "$root" "$key" || rc=$?
    if ((rc != 0)); then
      rr_log "tips: skipped '$key': not a repo key of pn-workspace.toml (or the file is unreadable)"
      continue
    fi
    if ! rr_path_check "$root" "$key"; then
      rr_log "tips: skipped '$key': $root/$key is not the top level of a git repository at that exact real path"
      continue
    fi
    branch="$(rr_repo_branch "$root" "$key")" || {
      rr_log "tips: skipped '$key': cannot read its branch"
      continue
    }
    if ! tip="$(rr_tip_of "$RR_REPO_PATH" "$branch")"; then
      rr_log "tips: skipped '$key': refs/heads/$branch does not exist"
      continue
    fi
    if rr_verdict_exists "$key" "$tip" check; then
      continue
    fi
    # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
    rr_jq -nc --arg repo "$key" --arg sha "$tip" --arg branch "$branch" \
      --arg at "$at" --arg exp "$expires" \
      '{id: ($repo + "@" + $sha), type: "repo-tip", title: ($repo + " tip"),
        metadata: {repo: $repo, sha: $sha, branch: $branch}, at: $at, expiresAt: $exp}' ||
      die "cannot emit the item for '$key'" 1
  done <<<"$keys"
  rr_log "tips: done"
  exit 0
}

# --- status -----------------------------------------------------------------

run_status() {
  local json=0 repo="" verdicts lease kill=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --json) json=1 ;;
    --repo)
      [[ $# -ge 2 ]] || die "--repo needs a value" 2
      repo="$2"
      shift
      ;;
    *) die "status: unknown argument: $1" 2 ;;
    esac
    shift
  done
  [[ -z $repo ]] || rr_valid_key "$repo" || die "status: bad repo key: $repo" 2
  rr_init_state || die "cannot create the state directory" 1
  verdicts="$(rr_verdicts_json "$repo")" || die "cannot read the verdict store" 1
  if ((json)); then
    printf '%s\n' "$verdicts"
    exit 0
  fi
  printf 'state dir: %s\n' "$RR_STATE_DIR"
  if [[ -e $RR_STATE_DIR/push.disabled || -L $RR_STATE_DIR/push.disabled ]]; then
    kill="$RR_STATE_DIR/push.disabled"
  fi
  printf 'kill switch: %s\n' "${kill:-absent}"
  if lease="$(readlink "$RR_LEASE" 2>/dev/null)"; then
    if rr_owner_alive "$lease"; then
      printf 'lease: held by %s\n' "$lease"
    else
      printf 'lease: stale (%s)\n' "$lease"
    fi
  else
    printf 'lease: free\n'
  fi
  if [[ $(rr_jq 'length' <<<"$verdicts") -eq 0 ]]; then
    printf 'verdicts: none\n'
  else
    printf 'verdicts:\n'
    rr_jq -r '.[] | [.repo, .sha[0:12], .kind, .result, .inputs, .finishedAt] | @tsv' <<<"$verdicts"
  fi
  exit 0
}

# --- dispatch ---------------------------------------------------------------

cmd=""
while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -*)
    rr_err "unknown option: $1"
    exit 2
    ;;
  *)
    cmd="$1"
    shift
    break
    ;;
  esac
done
[[ -n $cmd ]] || {
  rr_err "no command given (see --help)"
  exit 2
}

case "$cmd" in
tips) run_tips "$@" ;;
workspace-tips)
  rr_err "command '$cmd' is not implemented yet"
  exit 2
  ;;
verify | sync)
  [[ $# -eq 0 ]] || die "$cmd takes no arguments" 2
  run_role "$cmd" repo
  ;;
build)
  [[ $# -eq 0 ]] || die "build takes no arguments" 2
  run_role build workspace
  ;;
status) run_status "$@" ;;
*)
  rr_err "unknown command: $cmd (see --help)"
  exit 2
  ;;
esac
