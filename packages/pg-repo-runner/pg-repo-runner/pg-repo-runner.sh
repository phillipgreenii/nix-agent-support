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
# PG_REPO_RUNNER_GIT_BIN replaces the git binary (a test seam: the sync suite
# records every git call through a wrapper that execs the real git).
export RR_GIT_BIN="${PG_REPO_RUNNER_GIT_BIN:-${RR_GIT_BIN:-git}}"
export RR_JQ_BIN="${RR_JQ_BIN:-jq}"
export RR_TQ_BIN="${RR_TQ_BIN:-tq}"
export RR_NICE_BIN="${RR_NICE_BIN:-nice}"
# PG_REPO_RUNNER_NIX_BIN replaces the nix binary (a test seam, like the load
# average one: the bats suites use it to count and classify nix runs).
export RR_NIX_BIN="${PG_REPO_RUNNER_NIX_BIN:-${RR_NIX_BIN:-}}"
export RR_NIX_LOG_WRAPPED_BIN="${RR_NIX_LOG_WRAPPED_BIN:-}"
# bd and osascript are resolved from the ambient PATH (like every bd-calling script
# here; the machine wrapper carries the tracker guards). The PG_REPO_RUNNER_* names
# are the test seams for the failure-surfacing step: a real bd on a throwaway
# tracker, a recording stand-in for the macOS notifier.
export RR_BD_BIN="${PG_REPO_RUNNER_BD_BIN:-${RR_BD_BIN:-bd}}"
export RR_OSASCRIPT_BIN="${PG_REPO_RUNNER_OSASCRIPT_BIN:-${RR_OSASCRIPT_BIN:-/usr/bin/osascript}}"

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
  sync             Role: sync the repo named by PG_ROUTER_ITEM_ID (same): fetch,
                   classify, fast-forward a behind clone, surface divergence
                   (never pushes, rebases or resets)
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
  7. act       The role body (verify and sync are implemented; build is a stub
               that exits 2 in this version).

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

sync, in its act step (report-only; it has no push step): the canonical clone
MUST be steady (no core.worktree, on the primary branch, an empty
`git status --porcelain`, no rebase, merge, cherry-pick or revert in progress),
else the repo is skipped this pass ("yielded: canonical not steady") and never
repaired. Then `git fetch origin <primary>` (a remote-tracking ref update only)
and the classification of `git rev-list --left-right --count
origin/<primary>...<primary>`: 0,0 nothing; behind only `git merge --ff-only
origin/<primary>` (a refusal is recorded, not retried this pass); ahead only the
count is recorded and nothing else happens; diverged files ONE deduplicated bead
"diverged, resolve with pn workspace rebase or /pn-workspace-sync" (closed when
a later pass finds it no longer diverged). A fetch that fails is recorded and
exits 0. The result of every pass is in the sync record that `status` prints.

Failure surfacing (config [surface] enabled = true; off otherwise): a TRANSITION,
never a steady state, files ONE P1 bead (the repo's label from surface.repo_labels,
metadata runner = pg-repo-runner) in the tracker surface.tracker_dir and posts ONE
macOS notification: a repo's tip going red, diverged, push blocked: needs relock,
a red build. A repeated report files nothing more (a newer red sha is only
commented on the bead). A later tip that descends from the red one and is green
comments on the runner's OWN bead with the green verdict and closes it; a bead
without the marker is never touched. A bead or notification that could not be
delivered stays owed and is retried on the next run; the runner never files in
another tracker, and it never reverts, resets, edits or pushes a repo.

Exit codes:
  0  the run completed, whatever the verdict (a red check exits 0)
  9  busy: not now (kill switch, load, lease held); pg-router re-offers the item
  2  usage error, refusal, or a role that is not implemented yet
  1  malfunction (unreadable config, no load reading, state directory unusable)

status [--json] [--repo KEY]:
  Prints every stored verdict (optionally one repo's), whether the lease is free,
  held or stale, whether a kill file is present, the open failure transitions and
  the last sync result of each repo. --json prints only the
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
  PG_REPO_RUNNER_GIT_BIN   replaces the git binary (test seam)
  PG_REPO_RUNNER_BD_BIN    replaces the bd binary (default: bd on PATH)
  PG_REPO_RUNNER_OSASCRIPT_BIN   replaces the macOS notifier (test seam)
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
    # surfacing never changes the exit status of the run: a failure stays owed
    rr_surface_green "$repo" "$RR_REPO_PATH" "$full" \
      "Green verdict for $repo@$full (nix flake check, finished $finished, log $log). This tip descends from the red one." ||
      rr_log "surface: green handling failed for $repo@$full"
  else
    rr_verdict_write "$repo" "$full" check red locked "$started" "$finished" "$rc" "$log" "$extra" || return 1
    rr_surface_raise "$repo" red "$full" "red check verdict, exit $rc, log $log" ||
      rr_log "surface: raising the red transition failed for $repo@$full"
  fi
  rr_surface_flush_repo "$repo" || true
  return 0
}
# shellcheck disable=SC2329  # invoked indirectly as "act_$role"
act_sync() {
  local repo="$RR_ITEM_REPO" root branch path reason pre_counts pre_behind counts behind ahead tip detail rc

  path="$RR_REPO_PATH"
  root="$(rr_cfg '.workspace_root // empty')"
  branch="$(rr_repo_branch "$root" "$repo")" || {
    rr_err "cannot read the primary branch of '$repo' from pn-workspace.toml"
    return 1
  }
  rr_git check-ref-format "refs/heads/$branch" || {
    rr_err "the primary branch name of '$repo' is not a valid ref name: $branch"
    return 1
  }

  # 1. precheck, read-only: a canonical clone that is not steady is skipped this
  # pass and never repaired (R-3).
  if reason="$(rr_sync_steady "$path" "$branch")"; then :; else
    rr_sync_record "$repo" "$branch" yielded "" "" "" "canonical not steady: $reason" || return 1
    rr_log "sync: $repo yielded: canonical not steady: $reason"
    return 0
  fi

  # 2. fetch: a remote-tracking ref update only. The counts read BEFORE it are
  # the stale ones (kept in the record), the classification below re-reads.
  pre_counts="$(rr_sync_counts "$path" "$branch")" || pre_counts=""
  pre_behind="${pre_counts%%[[:space:]]*}"
  if ! rr_sync_fetch "$path" "$branch"; then
    rr_sync_record "$repo" "$branch" fetch-failed "" "" "$pre_behind" "git fetch origin $branch failed" || return 1
    rr_log "sync: $repo fetch-failed (not a malfunction of the runner: the next pass retries)"
    return 0
  fi

  # 3. classify, from the refs the fetch just updated
  if ! counts="$(rr_sync_counts "$path" "$branch")"; then
    rr_sync_record "$repo" "$branch" no-remote-ref "" "" "$pre_behind" "origin/$branch does not exist after the fetch" || return 1
    rr_log "sync: $repo has no origin/$branch after the fetch"
    return 0
  fi
  read -r behind ahead <<<"$counts"
  tip="$(rr_tip_of "$path" "$branch")" || {
    rr_err "the primary branch refs/heads/$branch of '$repo' does not exist"
    return 1
  }

  if ((behind == 0 && ahead == 0)); then
    rr_sync_record "$repo" "$branch" in-sync 0 0 "$pre_behind" "" || return 1
    rr_log "sync: $repo in sync"
    rr_sync_not_diverged "$repo" "$tip" "in sync with origin/$branch"
  elif ((behind > 0 && ahead == 0)); then
    rc=0
    rr_git -C "$path" merge --ff-only --quiet "refs/remotes/origin/$branch" >/dev/null 2>&1 || rc=$?
    if ((rc == 0)); then
      tip="$(rr_tip_of "$path" "$branch")" || tip=""
      rr_sync_record "$repo" "$branch" fast-forwarded "$behind" 0 "$pre_behind" "now at $tip" || return 1
      rr_log "sync: $repo fast-forwarded $behind commit(s) to $tip"
      rr_sync_not_diverged "$repo" "$tip" "fast-forwarded to origin/$branch"
    else
      rr_sync_record "$repo" "$branch" ff-refused "$behind" 0 "$pre_behind" "git merge --ff-only exited $rc; not retried this pass" || return 1
      rr_log "sync: $repo ff-refused (merge --ff-only exit $rc); not retried this pass"
    fi
  elif ((behind == 0 && ahead > 0)); then
    # A push candidate. There is no push step in this version: record the count
    # and do nothing else (no bead, no notification).
    rr_sync_record "$repo" "$branch" ahead 0 "$ahead" "$pre_behind" "" || return 1
    rr_log "sync: $repo is $ahead commit(s) ahead of origin/$branch; nothing to do"
    rr_sync_not_diverged "$repo" "$tip" "ahead of origin/$branch only"
  else
    detail="behind $behind, ahead $ahead of origin/$branch"
    rr_sync_record "$repo" "$branch" diverged "$behind" "$ahead" "$pre_behind" "$detail" || return 1
    rr_log "sync: $repo diverged ($detail); surfaced, not touched"
    rr_surface_raise "$repo" diverged "$tip" "$detail" ||
      rr_log "surface: raising the diverged transition failed for $repo"
  fi
  # a bead or notification still owed from an earlier pass is finished here
  rr_surface_flush_repo "$repo" || true
  return 0
}

# rr_sync_not_diverged REPO TIP EVIDENCE: the repo is not diverged any more;
# closes the runner's own open diverged bead, if there is one.
# shellcheck disable=SC2329  # called by act_sync, which is itself invoked indirectly
rr_sync_not_diverged() {
  rr_surface_resolve "$1" diverged "$2" "No longer diverged: $3 (tip $2)." ||
    rr_log "surface: resolving the diverged transition failed for $1"
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
    if [[ $role == verify && -z $RR_ITEM_SHA ]]; then
      die "verify needs an item id <repo>@<sha>, got: $item" 2
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
  local json=0 repo="" verdicts lease kill="" transitions syncs
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
  transitions="$(rr_surface_records_json)" || transitions='[]'
  if [[ -n $repo ]]; then
    # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
    transitions="$(rr_jq -c --arg r "$repo" 'map(select(.repo == $r))' <<<"$transitions")"
  fi
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  transitions="$(rr_jq -c 'map(select(.state == "open"))' <<<"$transitions")"
  if [[ $(rr_jq 'length' <<<"$transitions") -eq 0 ]]; then
    printf 'open transitions: none\n'
  else
    printf 'open transitions:\n'
    rr_jq -r '.[] | [.repo, .kind, .lastSha[0:12], (.bead | if . == "" then "bead-pending" else . end), (if .notified then "notified" else "notify-pending" end)] | @tsv' <<<"$transitions"
  fi
  syncs="$(rr_sync_records_json)" || syncs='[]'
  if [[ -n $repo ]]; then
    # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
    syncs="$(rr_jq -c --arg r "$repo" 'map(select(.repo == $r))' <<<"$syncs")"
  fi
  if [[ $(rr_jq 'length' <<<"$syncs") -eq 0 ]]; then
    printf 'sync: none\n'
  else
    printf 'sync:\n'
    rr_jq -r '.[] | [.repo, .outcome, ("behind " + (.behind | tostring)), ("ahead " + (.ahead | tostring)), ("behind-before-fetch " + (.behindBeforeFetch | tostring)), .at] | @tsv' <<<"$syncs"
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
