# shellcheck shell=bash
# pg-repo-runner kernel (bead pg2-zj2v7m, F1 of the async-runners design):
# the Template Method every runner role shares. Order of the steps a role
# goes through: resolve the repo key, check the path, honor the kill switch,
# check the machine load, take the single-flight lease, act, record a verdict,
# release. This file holds the steps as functions (prefix rr_); the entry
# point pg-repo-runner.sh sequences them. Roles supply only "act".
#
# Generic by design: it names no employer and no machine. Every value comes
# from the config file the home module renders, or from the environment.
#
# Binaries the kernel offers are resolved through RR_*_BIN variables, which the
# nix build injects as absolute store paths; a raw run (the bats suite, "tests
# MUST work without nix build") falls back to the bare name on PATH.
#
# Exit codes of the entry point: 0 for any verdict, 9 for "not now" (kill switch,
# load, lease held; pg-router re-offers the item), 2 for a usage error or a
# refusal, 1 for a malfunction.

# --- small wrappers over external tools -----------------------------------

rr_err() { printf 'pg-repo-runner: %s\n' "$*" >&2; }

rr_jq() { "${RR_JQ_BIN:-jq}" "$@"; }

# rr_tq FILE: a TOML file as one JSON document on stdout (tomlq's `tq`).
rr_tq() { "${RR_TQ_BIN:-tq}" -o json -f "$1" .; }

# git with the repository-location variables unset: an inherited GIT_DIR (a hook
# or worktree environment) would pin discovery there and bypass `-C`.
rr_git() {
  env -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR -u GIT_INDEX_FILE \
    -u GIT_PREFIX -u GIT_OBJECT_DIRECTORY "${RR_GIT_BIN:-git}" "$@"
}

rr_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# --- state directory and run log ------------------------------------------

# Sets RR_STATE_DIR, RR_RUN_LOG and RR_LEASE and creates the directory tree.
# The state directory is $XDG_STATE_HOME/pg-repo-runner (never a path under a
# repo): the verdict store, the run log, the lease and the kill file live there.
rr_init_state() {
  local base
  base="${XDG_STATE_HOME:-${HOME:?HOME is not set}/.local/state}"
  RR_STATE_DIR="$base/pg-repo-runner"
  RR_RUN_LOG="$RR_STATE_DIR/run.log"
  RR_LEASE="$RR_STATE_DIR/lease"
  mkdir -p "$RR_STATE_DIR" "$RR_STATE_DIR/verdicts" && chmod 0700 "$RR_STATE_DIR"
}

# rr_log MESSAGE...: one line on the run log. pg-router discards a role's stdout
# and stderr, so this file is where a run is explained. A failure to write it
# never fails the run.
rr_log() {
  [[ -n ${RR_RUN_LOG:-} ]] || return 0
  printf '%s pid=%s role=%s item=%s %s\n' "$(rr_now)" "$$" "${RR_ROLE:--}" \
    "${PG_ROUTER_ITEM_ID:--}" "$*" >>"$RR_RUN_LOG" 2>/dev/null || true
}

# --- configuration --------------------------------------------------------

rr_config_path() {
  printf '%s' "${PG_REPO_RUNNER_CONFIG:-${XDG_CONFIG_HOME:-${HOME:?HOME is not set}/.config}/pg-repo-runner/config.toml}"
}

# Reads the config TOML into RR_CONFIG_JSON. Returns 1 when it is unreadable
# or malformed.
rr_load_config() {
  local path
  path="$(rr_config_path)"
  if [[ ! -r $path ]]; then
    rr_err "config is not readable: $path"
    return 1
  fi
  if ! RR_CONFIG_JSON="$(rr_tq "$path" 2>/dev/null)"; then
    rr_err "config is not valid TOML: $path"
    return 1
  fi
}

# rr_cfg JQ-FILTER: a raw value from the loaded config.
rr_cfg() { rr_jq -r "$1" <<<"$RR_CONFIG_JSON"; }

# rr_repo_listed KEY: the repo is in the config allow-list (repos.<key>).
rr_repo_listed() {
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rr_jq -e --arg k "$1" '(.repos // {}) | has($k)' <<<"$RR_CONFIG_JSON" >/dev/null
}

# rr_repo_flag KEY FLAG: repos.<key>.<flag> is exactly true.
rr_repo_flag() {
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rr_jq -e --arg k "$1" --arg f "$2" '.repos[$k][$f] == true' <<<"$RR_CONFIG_JSON" >/dev/null
}

# --- resolve and path check -----------------------------------------------

# rr_valid_key KEY: a plain directory name (no slash, no dot-dot, no leading dot).
rr_valid_key() {
  [[ $1 =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] && [[ $1 != *..* ]]
}

# rr_valid_sha SHA: lowercase hex, 7 to 64 characters (a git object name, or the
# hash of a tip tuple for a build verdict).
rr_valid_sha() {
  [[ $1 =~ ^[0-9a-f]{7,64}$ ]]
}

# rr_parse_item_id ID: ID is `<key>` or `<key>@<sha>` (the shape pg-router
# exports as PG_ROUTER_ITEM_ID). Sets RR_ITEM_REPO and RR_ITEM_SHA (empty when
# the id carries no sha). Returns 1 on anything else.
rr_parse_item_id() {
  local id="$1" key sha=""
  key="${id%%@*}"
  [[ $id == *@* ]] && sha="${id#*@}"
  rr_valid_key "$key" || return 1
  if [[ -n $sha ]]; then rr_valid_sha "$sha" || return 1; fi
  [[ $id == *@* && -z $sha ]] && return 1
  # shellcheck disable=SC2034  # read by the roles and the entry point
  RR_ITEM_REPO="$key"
  # shellcheck disable=SC2034  # read by the roles and the entry point
  RR_ITEM_SHA="$sha"
}

# rr_workspace_has_key ROOT KEY: KEY is a repo key of ROOT/pn-workspace.toml.
# Returns 0 when it is, 1 when it is not, 2 when the file cannot be read.
rr_workspace_has_key() {
  local toml="$1/pn-workspace.toml" json
  [[ -r $toml ]] || return 2
  json="$(rr_tq "$toml" 2>/dev/null)" || return 2
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rr_jq -e --arg k "$2" '(.repos // {}) | has($k)' <<<"$json" >/dev/null
}

# rr_path_check ROOT KEY: succeeds only when the realpath of ROOT/KEY IS
# <realpath ROOT>/KEY (so a symlinked key is refused) and that directory is the
# top level of a git repository (so a plain directory, or a subdirectory of some
# other repository, is refused). Sets RR_REPO_PATH.
rr_path_check() {
  local root_real expected actual top
  root_real="$(realpath -e -- "$1")" || return 1
  expected="$root_real/$2"
  actual="$(realpath -e -- "$expected" 2>/dev/null)" || return 1
  [[ $actual == "$expected" ]] || return 1
  top="$(rr_git -C "$actual" rev-parse --show-toplevel 2>/dev/null)" || return 1
  top="$(realpath -e -- "$top" 2>/dev/null)" || return 1
  [[ $top == "$actual" ]] || return 1
  # shellcheck disable=SC2034  # read by the roles and the entry point
  RR_REPO_PATH="$actual"
}

# --- kill switch ----------------------------------------------------------

# Prints the first kill file that exists and succeeds, else returns 1. Two files
# count: <state dir>/push.disabled, and the path the config names as
# `kill_switch`. Either one stops every role (and a hand run).
rr_kill_switch_active() {
  local f cfg
  cfg="$(rr_cfg '.kill_switch // empty')"
  for f in "$RR_STATE_DIR/push.disabled" "$cfg"; do
    if [[ -n $f && (-e $f || -L $f) ]]; then
      printf '%s' "$f"
      return 0
    fi
  done
  return 1
}

# --- load check -----------------------------------------------------------

# Prints the 1-minute load average. PG_REPO_RUNNER_LOADAVG replaces the real
# reading (the documented seam the bats suite uses). Returns 1 when no reading
# can be had.
rr_loadavg1() {
  local v=""
  if [[ -n ${PG_REPO_RUNNER_LOADAVG:-} ]]; then
    v="$PG_REPO_RUNNER_LOADAVG"
  elif [[ -r /proc/loadavg ]]; then
    read -r v _ </proc/loadavg
  else
    v="$("${RR_SYSCTL_BIN:-/usr/sbin/sysctl}" -n vm.loadavg 2>/dev/null | tr -d '{}' | tr -s ' ' | sed 's/^ //' | cut -d' ' -f1)"
  fi
  [[ $v =~ ^[0-9]+(\.[0-9]+)?$ ]] || return 1
  printf '%s' "$v"
}

# Prints the number of cores (PG_REPO_RUNNER_NCPU replaces the real count).
rr_ncpu() {
  local n="${PG_REPO_RUNNER_NCPU:-}"
  [[ -n $n ]] || n="$(nproc 2>/dev/null)" || return 1
  [[ $n =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s' "$n"
}

# Prints the load threshold: PG_REPO_RUNNER_MAX_LOAD, else the config's
# thresholds.max_load, else the core count. A run is admitted only while the
# 1-minute load average is strictly below it.
rr_max_load() {
  local v="${PG_REPO_RUNNER_MAX_LOAD:-}"
  [[ -n $v ]] || v="$(rr_cfg '.thresholds.max_load // empty')"
  [[ -n $v ]] || v="$(rr_ncpu)" || return 1
  [[ $v =~ ^[0-9]+(\.[0-9]+)?$ ]] || return 1
  printf '%s' "$v"
}

# rr_to_centi DECIMAL: hundredths as an integer ("1.5" is 150, "11" is 1100), so
# the comparison needs no floating point and no awk.
rr_to_centi() {
  local v="$1" i f
  i="${v%%.*}"
  f="00"
  [[ $v == *.* ]] && f="${v#*.}00"
  f="${f:0:2}"
  printf '%s' "$((10#$i * 100 + 10#$f))"
}

# rr_load_admits LOAD MAX: succeeds when LOAD is strictly below MAX.
rr_load_admits() {
  (($(rr_to_centi "$1") < $(rr_to_centi "$2")))
}

# --- single-flight lease --------------------------------------------------

# One lease, shared by every role: a symlink at $RR_LEASE whose target is
# "<pid>:<process start time>" of the owner. `ln -s` is atomic and fails when the
# name exists, so creating the link IS taking the lease. It is never waited for.
# A lease is STALE when its pid is dead or now belongs to a process with a
# different start time (a recycled pid); a stale lease is moved aside (rename is
# atomic, so one reclaimer wins) and taken. The same pattern pg-test-runner uses
# for its run slots.

# rr_proc_stamp PID: the start time of PID, whitespace-normalised; fails when
# the process does not exist.
rr_proc_stamp() {
  local out
  out="$(ps -o lstart= -p "$1" 2>/dev/null)" || return 1
  [[ -n $out ]] || return 1
  tr -s ' ' <<<"$out"
}

# rr_owner_alive OWNER: OWNER is "<pid>:<start time>"; succeeds when that exact
# process is still running.
rr_owner_alive() {
  local owner="$1" pid stamp now
  pid="${owner%%:*}"
  stamp="${owner#*:}"
  [[ $pid =~ ^[0-9]+$ ]] || return 1
  now="$(rr_proc_stamp "$pid")" || return 1
  [[ $now == "$stamp" ]]
}

# Takes the lease without waiting. Returns 0 when held by this process, 1 when a
# live owner holds it, 2 when the state directory cannot be used.
rr_lease_acquire() {
  local stamp owner target aside
  stamp="$(rr_proc_stamp "$$")" || return 2
  owner="$$:$stamp"
  mkdir -p "$RR_STATE_DIR" 2>/dev/null || return 2
  if ln -s "$owner" "$RR_LEASE" 2>/dev/null; then
    RR_LEASE_OWNER="$owner"
    return 0
  fi
  target="$(readlink "$RR_LEASE" 2>/dev/null)" || return 1
  rr_owner_alive "$target" && return 1
  # Stale: rename it aside first, re-check what was actually moved, then take it.
  aside="$RR_STATE_DIR/.reclaim.$$"
  if mv "$RR_LEASE" "$aside" 2>/dev/null; then
    if rr_owner_alive "$(readlink "$aside" 2>/dev/null)"; then
      # Lost a race: the moved link belongs to a live owner. Put it back (BSD
      # `mv -n` exits 0 without moving when the target exists, so always sweep
      # the leftover afterwards).
      mv -n "$aside" "$RR_LEASE" 2>/dev/null
      rm -f "$aside"
      return 1
    fi
    rm -f "$aside"
    if ln -s "$owner" "$RR_LEASE" 2>/dev/null; then
      RR_LEASE_OWNER="$owner"
      return 0
    fi
  fi
  return 1
}

# Releases the lease, but only when it is still ours (a reclaimer may have taken
# it had we been wrongly judged dead).
# shellcheck disable=SC2329  # run from the EXIT trap string
rr_lease_release() {
  local target
  [[ -n ${RR_LEASE_OWNER:-} ]] || return 0
  target="$(readlink "$RR_LEASE" 2>/dev/null)" || return 0
  [[ $target == "$RR_LEASE_OWNER" ]] && rm -f "$RR_LEASE"
  RR_LEASE_OWNER=""
  return 0
}

# --- verdict store (Memento) ----------------------------------------------

# rr_verdict_write REPO SHA KIND RESULT INPUTS STARTED FINISHED EXIT LOGPATH
# writes $RR_STATE_DIR/verdicts/<repo>/<sha>.<kind>.json, atomically: the
# document is built in a temp file in the same directory, then renamed over the
# final name, so a reader sees the old file or the new one and a failure leaves
# no temp file behind. KIND is check|build, RESULT is green|red|error, INPUTS is
# locked|override, STARTED and FINISHED are RFC 3339 times, EXIT is the exit
# status of the work, LOGPATH is where its output went.
# shellcheck disable=SC2329  # called by the role bodies of the later beads
rr_verdict_write() {
  local repo="$1" sha="$2" kind="$3" result="$4" inputs="$5"
  local started="$6" finished="$7" exitc="$8" logp="$9" dir tmp
  rr_valid_key "$repo" || {
    rr_err "verdict: bad repo key: $repo"
    return 2
  }
  rr_valid_sha "$sha" || {
    rr_err "verdict: bad sha: $sha"
    return 2
  }
  case "$kind" in check | build) ;; *)
    rr_err "verdict: bad kind: $kind"
    return 2
    ;;
  esac
  case "$result" in green | red | error) ;; *)
    rr_err "verdict: bad result: $result"
    return 2
    ;;
  esac
  case "$inputs" in locked | override) ;; *)
    rr_err "verdict: bad inputs: $inputs"
    return 2
    ;;
  esac
  [[ $exitc =~ ^[0-9]+$ ]] || {
    rr_err "verdict: bad exit: $exitc"
    return 2
  }
  dir="$RR_STATE_DIR/verdicts/$repo"
  mkdir -p "$dir" || return 1
  tmp="$(mktemp "$dir/.$sha.$kind.XXXXXX")" || return 1
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  if ! rr_jq -n \
    --arg repo "$repo" --arg sha "$sha" --arg kind "$kind" --arg result "$result" \
    --arg inputs "$inputs" --arg startedAt "$started" --arg finishedAt "$finished" \
    --argjson exit "$exitc" --arg logPath "$logp" \
    '{repo: $repo, sha: $sha, kind: $kind, result: $result, inputs: $inputs,
      startedAt: $startedAt, finishedAt: $finishedAt, exit: $exit, logPath: $logPath}' \
    >"$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  if ! mv -f "$tmp" "$dir/$sha.$kind.json"; then
    rm -f "$tmp"
    return 1
  fi
}

# rr_verdicts_json [REPO]: every stored verdict (optionally one repo's) as one
# JSON array, ordered by repo then finish time. Temp files are never read.
rr_verdicts_json() {
  local repo="${1:-}" root f
  local -a files=()
  root="$RR_STATE_DIR/verdicts"
  [[ -n $repo ]] && root="$root/$repo"
  if [[ -d $root ]]; then
    while IFS= read -r -d '' f; do
      files+=("$f")
    done < <(find "$root" -type f -name '*.json' ! -name '.*' -print0 | sort -z)
  fi
  if ((${#files[@]} == 0)); then
    echo '[]'
    return 0
  fi
  rr_jq -s 'sort_by(.repo, .finishedAt, .kind)' "${files[@]}"
}

# --- nix, as the kernel offers it -----------------------------------------

# The nice level for nix runs: the config's thresholds.nice_level, default 10.
# shellcheck disable=SC2329  # used by rr_nix_run
rr_nice_level() {
  local v
  v="$(rr_cfg '.thresholds.nice_level // empty' 2>/dev/null)" || v=""
  [[ $v =~ ^([0-9]|1[0-9])$ ]] || v=10
  printf '%s' "$v"
}

# rr_nix_run ARGS...: runs `nix ARGS...` the one way a runner may: under `nice`,
# through pg-nix-log-wrapped, every binary by absolute path (RR_NICE_BIN,
# RR_NIX_LOG_WRAPPED_BIN and RR_NIX_BIN, injected by the nix build). A build
# without the wrapper runs nix directly and says so (the workspace rule: never
# block on a missing wrapper).
# shellcheck disable=SC2329  # called by the role bodies of the later beads
rr_nix_run() {
  local nice="${RR_NICE_BIN:-nice}" nix="${RR_NIX_BIN:-}" wrapped="${RR_NIX_LOG_WRAPPED_BIN:-}" level
  if [[ -z $nix ]]; then
    rr_err "no nix binary is configured (RR_NIX_BIN)"
    return 1
  fi
  level="$(rr_nice_level)"
  if [[ -n $wrapped ]]; then
    "$nice" -n "$level" "$wrapped" "$nix" "$@"
  else
    rr_err "pg-nix-log-wrapped is not configured; running nix without it"
    "$nice" -n "$level" "$nix" "$@"
  fi
}
