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

# rr_verdict_write REPO SHA KIND RESULT INPUTS STARTED FINISHED EXIT LOGPATH [EXTRA]
# writes $RR_STATE_DIR/verdicts/<repo>/<sha>.<kind>.json, atomically: the
# document is built in a temp file in the same directory, then renamed over the
# final name, so a reader sees the old file or the new one and a failure leaves
# no temp file behind. KIND is check|build, RESULT is green|red|error|superseded
# (superseded: the repo's tip moved before the check ran, so nothing was run),
# INPUTS is locked|override, STARTED and FINISHED are RFC 3339 times, EXIT is the
# exit status of the work, LOGPATH is where its output went. EXTRA, when given,
# is a JSON object merged into the record (the verify role adds the measured
# duration and the sampled load).
# shellcheck disable=SC2329  # called by the role bodies
rr_verdict_write() {
  local repo="$1" sha="$2" kind="$3" result="$4" inputs="$5"
  local started="$6" finished="$7" exitc="$8" logp="$9" extra="${10:-}" dir tmp
  [[ -n $extra ]] || extra='{}'
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
  rr_jq -e 'type == "object"' <<<"$extra" >/dev/null 2>&1 || {
    rr_err "verdict: extra is not a JSON object"
    return 2
  }
  case "$result" in green | red | error | superseded) ;; *)
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
    --argjson exit "$exitc" --arg logPath "$logp" --argjson extra "$extra" \
    '{repo: $repo, sha: $sha, kind: $kind, result: $result, inputs: $inputs,
      startedAt: $startedAt, finishedAt: $finishedAt, exit: $exit, logPath: $logPath} + $extra' \
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

# --- tips and verify helpers (runner B) -----------------------------------

# rr_epoch: seconds since the epoch. PG_REPO_RUNNER_EPOCH replaces the clock (the
# documented seam the debounce tests use to walk through ticks).
rr_epoch() {
  if [[ ${PG_REPO_RUNNER_EPOCH:-} =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$PG_REPO_RUNNER_EPOCH"
  else
    date -u +%s
  fi
}

# rr_iso_at EPOCH: an RFC 3339 UTC time.
rr_iso_at() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

# rr_retry_window: how long an emitted tip item stays offered, in seconds
# (PG_REPO_RUNNER_RETRY_WINDOW, else the config's thresholds.retry_window, else
# 3600). It is also the de-duplication window of the item id.
rr_retry_window() {
  local v="${PG_REPO_RUNNER_RETRY_WINDOW:-}"
  [[ -n $v ]] || v="$(rr_cfg '.thresholds.retry_window // empty')"
  [[ $v =~ ^[1-9][0-9]*$ ]] || v=3600
  printf '%s' "$v"
}

# rr_verdict_exists REPO SHA KIND: a verdict file is stored for exactly that key.
rr_verdict_exists() {
  [[ -f $RR_STATE_DIR/verdicts/$1/$2.$3.json ]]
}

# rr_repo_branch ROOT KEY: the primary branch of KEY, from pn-workspace.toml
# (repos.<key>.branch), `main` when it names none.
rr_repo_branch() {
  local json
  json="$(rr_tq "$1/pn-workspace.toml" 2>/dev/null)" || return 1
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rr_jq -r --arg k "$2" '.repos[$k].branch // "main"' <<<"$json"
}

# rr_tip_of PATH BRANCH: the full sha of refs/heads/BRANCH, read from git objects
# only (no working tree is read). Fails when the branch does not exist.
rr_tip_of() {
  rr_git -C "$1" rev-parse --verify -q "refs/heads/$2^{commit}"
}

# rr_signal_exit RC: succeeds when RC is 128 plus a signal number, a process the
# runner could not see finish (killed under overload): a malfunction, never red.
rr_signal_exit() {
  [[ $1 =~ ^[0-9]+$ ]] && (($1 > 128 && $1 <= 192))
}

# rr_url_path_encode PATH: PATH percent-encoded for the path of a flake URL. Only
# the characters that mean something in a URL (and non-ASCII bytes) are encoded.
rr_url_path_encode() {
  local s="$1" out="" c i
  local LC_ALL=C
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in
    [A-Za-z0-9/._~-]) out+="$c" ;;
    *) out+="$(printf '%%%02X' "'$c")" ;;
    esac
  done
  printf '%s' "$out"
}

# rr_snapshot_url PATH SHA: the flake reference of the commit SHA of the repository
# at PATH, as an immutable git snapshot (git objects only, no working tree).
rr_snapshot_url() {
  printf 'git+file://%s?rev=%s' "$(rr_url_path_encode "$1")" "$2"
}

# --- failure surfacing (F3) ----------------------------------------------
#
# The kernel surfaces TRANSITIONS, never steady states (design "Failure
# surfacing", decision D7): a repo going red, `diverged`, `push blocked: needs
# relock`, a red `build`. Each transition gets at most ONE bead (P1, the repo's
# label, metadata runner = pg-repo-runner) and ONE notification, however often
# the same state is reported again. The state of every (repo, kind) pair is a
# Memento in $RR_STATE_DIR/surface/<repo>.<kind>.json, so a crash, an
# unreachable tracker or a failed notification leaves the transition PENDING
# and the next pass finishes exactly the missing part (BF-2: never another
# tracker). A later tip that descends from the red one and is green closes the
# runner's OWN bead with the green verdict as evidence; a bead without the
# runner marker is never touched. Nothing here reverts, resets or edits a repo
# (fix-forward) and nothing pushes.
#
# Surfacing is off unless the config carries `[surface] enabled = true`.

# rr_surface_kind_valid KIND: one of the transitions the design names.
rr_surface_kind_valid() {
  case "$1" in red | diverged | needs-relock | build-red) return 0 ;; esac
  return 1
}

# rr_surface_enabled: the config enables surfacing.
rr_surface_enabled() {
  [[ "$(rr_cfg '.surface.enabled // false' 2>/dev/null)" == true ]]
}

# rr_surface_file REPO KIND: the path of the transition's Memento.
rr_surface_file() { printf '%s/surface/%s.%s.json' "$RR_STATE_DIR" "$1" "$2"; }

# rr_surface_load REPO KIND: the stored record, or `{}`.
rr_surface_load() {
  local f
  f="$(rr_surface_file "$1" "$2")"
  if [[ -f $f ]]; then cat "$f"; else echo '{}'; fi
}

# rr_surface_store REPO KIND JSON: atomic write (temp file, then rename).
rr_surface_store() {
  local f tmp dir
  f="$(rr_surface_file "$1" "$2")"
  dir="$(dirname "$f")"
  mkdir -p "$dir" || return 1
  tmp="$(mktemp "$dir/.$1.$2.XXXXXX")" || return 1
  if ! printf '%s\n' "$3" >"$tmp" || ! mv -f "$tmp" "$f"; then
    rm -f "$tmp"
    return 1
  fi
}

# rr_surface_records_json: every stored transition record as one JSON array.
rr_surface_records_json() {
  local f
  local -a files=()
  if [[ -d $RR_STATE_DIR/surface ]]; then
    while IFS= read -r -d '' f; do
      files+=("$f")
    done < <(find "$RR_STATE_DIR/surface" -type f -name '*.json' ! -name '.*' -print0 | sort -z)
  fi
  if ((${#files[@]} == 0)); then
    echo '[]'
    return 0
  fi
  rr_jq -s 'sort_by(.repo, .kind)' "${files[@]}"
}

# rr_bd ARGS...: bd against the configured tracker and no other (BF-2: no
# fallback). BEADS_DIR is dropped so the config, not an inherited variable,
# picks the tracker; dolt is never auto-started.
rr_bd() {
  local tracker
  tracker="$(rr_cfg '.surface.tracker_dir // empty')"
  if [[ -z $tracker ]]; then
    rr_err "surface.tracker_dir is not configured"
    return 1
  fi
  env -u BEADS_DIR BEADS_DOLT_AUTO_START=0 "${RR_BD_BIN:-bd}" -C "$tracker" --actor pg-repo-runner "$@"
}

# rr_bd_norm: bd --json on stdin, normalised to the bare value whether or not
# BD_JSON_ENVELOPE wrapped it in {"data": ...}.
rr_bd_norm() {
  rr_jq -c 'if type == "object" and has("data") then .data else . end'
}

# rr_surface_label REPO: the repo label from surface.repo_labels, empty when none.
rr_surface_label() {
  local label root term
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  label="$(rr_jq -r --arg k "$1" '.surface.repo_labels[$k] // empty' <<<"$RR_CONFIG_JSON")"
  if [[ -z $label && $1 == workspace ]]; then
    # the workspace build has no repo of its own: it is filed under the label of the
    # workspace's terminal repo (the repo whose host closure the build realizes),
    # unless surface.repo_labels names a `workspace` label explicitly
    root="$(rr_cfg '.workspace_root // empty')"
    if term="$(rr_tq "$root/pn-workspace.toml" 2>/dev/null | rr_jq -r '.workspace.terminal // empty')" && [[ -n $term ]]; then
      # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
      label="$(rr_jq -r --arg k "$term" '.surface.repo_labels[$k] // empty' <<<"$RR_CONFIG_JSON")"
    fi
  fi
  printf '%s' "$label"
}

# rr_surface_title REPO KIND SHA: the bead title.
rr_surface_title() {
  local repo="$1" kind="$2" sha="${3:0:12}"
  case "$kind" in
  red) printf '%s: primary tip %s fails nix flake check' "$repo" "$sha" ;;
  diverged) printf '%s: diverged from its remote, resolve with pn workspace rebase or /pn-workspace-sync' "$repo" ;;
  needs-relock) printf '%s: push blocked: needs relock' "$repo" ;;
  build-red) printf '%s: workspace build is red at %s' "$repo" "$sha" ;;
  esac
}

# rr_surface_body REPO KIND SHA DETAIL: the self-contained bead description.
rr_surface_body() {
  local repo="$1" kind="$2" sha="$3" detail="$4"
  printf 'pg-repo-runner saw a transition on %s: %s (at %s).\n\n' "$repo" "$kind" "$sha"
  [[ -z $detail ]] || printf 'Evidence: %s\n\n' "$detail"
  printf 'Read-only facts: `pg-repo-runner status` lists the verdicts and the open transitions.\n\n'
  if [[ $kind == diverged ]]; then
    printf 'The primary branch has commits the remote lacks AND the remote has commits it lacks. '
    printf 'The runner never rebases, resets or forces: resolve it from a session or the operator '
    printf 'shell with `pn workspace rebase` or `/pn-workspace-sync`. The runner closes this bead '
    printf 'on its next pass that finds the branch no longer diverged (it closes only beads '
    printf 'carrying metadata runner = pg-repo-runner).\n'
    return 0
  fi
  printf 'The expectation is FIX-FORWARD: land a fix, which produces a new tip and a new verdict. '
  printf 'The runner never reverts, resets or edits a repo. When a later tip that descends from '
  printf 'this one is green, the runner comments here with the green verdict and closes this bead '
  printf '(it closes only beads carrying metadata runner = pg-repo-runner).\n'
}

# rr_bead_find REPO KIND: prints the id of the runner's own open bead for the
# pair (crash recovery: the bead may exist although its id was never stored).
# Returns 1 when the tracker cannot be queried.
rr_bead_find() {
  local out
  out="$(rr_bd list --metadata-field runner=pg-repo-runner --metadata-field "repo=$1" \
    --metadata-field "kind=$2" --status open,in_progress,blocked,deferred -n 0 --json 2>/dev/null)" || return 1
  rr_bd_norm <<<"$out" | rr_jq -r 'if type == "array" then (.[0].id // empty) else empty end'
}

# rr_bead_create REPO KIND SHA DETAIL: files the P1 bead and prints its id.
# Refuses (return 1) without a repo label: a bead with no repo label would be
# invisible to its owner.
rr_bead_create() {
  local repo="$1" kind="$2" sha="$3" detail="$4" label meta out
  label="$(rr_surface_label "$repo")"
  if [[ -z $label ]]; then
    rr_err "surface: no repo label for '$repo' in surface.repo_labels: no bead is filed"
    return 1
  fi
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  meta="$(rr_jq -nc --arg repo "$repo" --arg kind "$kind" --arg sha "$sha" \
    '{runner: "pg-repo-runner", repo: $repo, kind: $kind, sha: $sha}')" || return 1
  out="$(rr_bd create "$(rr_surface_title "$repo" "$kind" "$sha")" --type task --priority 1 \
    --labels "$label" --metadata "$meta" --description "$(rr_surface_body "$repo" "$kind" "$sha" "$detail")" \
    --json 2>/dev/null)" || return 1
  rr_bd_norm <<<"$out" | rr_jq -r 'if type == "array" then .[0].id else .id end // empty'
}

# rr_bead_close_own ID EVIDENCE: comments EVIDENCE on bead ID and closes it, but
# only when it carries metadata runner = pg-repo-runner. Returns 0 when the bead
# is closed (by this call or earlier) or is not ours (left alone), 1 when the
# tracker could not be reached or refused.
rr_bead_close_own() {
  local id="$1" evidence="$2" out owner status
  out="$(rr_bd show "$id" --json 2>/dev/null)" || return 1
  out="$(rr_bd_norm <<<"$out" | rr_jq -c 'if type == "array" then .[0] else . end')" || return 1
  owner="$(rr_jq -r '.metadata.runner // ""' <<<"$out")"
  status="$(rr_jq -r '.status // ""' <<<"$out")"
  if [[ $owner != pg-repo-runner ]]; then
    rr_log "surface: bead $id is not owned by pg-repo-runner; left alone"
    return 0
  fi
  [[ $status == closed ]] && return 0
  rr_bd comments add "$id" "$evidence" >/dev/null 2>&1 || return 1
  rr_bd close "$id" --reason "$evidence" >/dev/null 2>&1 || return 1
}

# rr_notify TITLE BODY: a macOS notification. TITLE and BODY travel as argv items
# of a FIXED AppleScript, never inside the script text, so nothing in them can be
# run as AppleScript.
rr_notify() {
  "${RR_OSASCRIPT_BIN:-/usr/bin/osascript}" \
    -e 'on run argv' \
    -e 'display notification (item 2 of argv) with title (item 1 of argv)' \
    -e 'end run' "$1" "$2" >/dev/null 2>&1
}

# rr_surface_flush REPO KIND: finishes whatever the transition still owes, each
# part recorded as done the moment it succeeds: (1) a requested close, (2) the
# bead, (3) the notification. A part that fails stays owed and is retried on the
# next pass; a failure never fails the run.
rr_surface_flush() {
  local repo="$1" kind="$2" rec bead notified close_sha evidence id sha detail now title
  rec="$(rr_surface_load "$repo" "$kind")"
  [[ "$(rr_jq -r '.state // ""' <<<"$rec")" == open ]] || return 0
  bead="$(rr_jq -r '.bead // ""' <<<"$rec")"
  notified="$(rr_jq -r '.notified // false' <<<"$rec")"
  now="$(rr_now)"

  close_sha="$(rr_jq -r '.pendingClose.sha // ""' <<<"$rec")"
  if [[ -n $close_sha ]]; then
    evidence="$(rr_jq -r '.pendingClose.evidence // ""' <<<"$rec")"
    if [[ -n $bead ]] && ! rr_bead_close_own "$bead" "$evidence"; then
      rr_log "surface: $repo $kind: closing $bead is pending (tracker unreachable)"
      return 0
    fi
    # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
    rec="$(rr_jq -c --arg now "$now" '.state = "clear" | .updatedAt = $now | .clearedBy = .pendingClose.sha | del(.pendingClose)' <<<"$rec")"
    rr_surface_store "$repo" "$kind" "$rec"
    rr_log "surface: $repo $kind cleared by $close_sha${bead:+ (bead $bead closed)}"
    return 0
  fi

  sha="$(rr_jq -r '.sha' <<<"$rec")"
  detail="$(rr_jq -r '.detail // ""' <<<"$rec")"
  if [[ -z $bead ]]; then
    if id="$(rr_bead_find "$repo" "$kind")" && [[ -z $id ]]; then
      id="$(rr_bead_create "$repo" "$kind" "$sha" "$detail")" || id=""
    fi
    if [[ -n ${id:-} ]]; then
      bead="$id"
      # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
      rec="$(rr_jq -c --arg b "$bead" --arg now "$now" '.bead = $b | .updatedAt = $now' <<<"$rec")"
      rr_surface_store "$repo" "$kind" "$rec" || return 0
      rr_log "surface: $repo $kind: bead $bead"
    else
      rr_log "surface: $repo $kind: the bead is pending (no tracker, no repo label, or tracker unreachable)"
    fi
  fi
  if [[ $notified != true ]]; then
    title="pg-repo-runner: $repo $kind"
    if [[ "$(rr_cfg '.surface.notify | if . == null then true else . end')" != true ]] ||
      rr_notify "$title" "$(rr_surface_title "$repo" "$kind" "$sha")${bead:+ ($bead)}"; then
      # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
      rec="$(rr_jq -c --arg now "$now" '.notified = true | .updatedAt = $now' <<<"$(rr_surface_load "$repo" "$kind")")"
      rr_surface_store "$repo" "$kind" "$rec" || return 0
    else
      rr_log "surface: $repo $kind: the notification is pending (osascript failed)"
    fi
  fi
  return 0
}

# rr_surface_flush_repo REPO: rr_surface_flush for every kind of REPO.
rr_surface_flush_repo() {
  local kind
  rr_surface_enabled || return 0
  for kind in red diverged needs-relock build-red; do
    rr_surface_flush "$1" "$kind"
  done
}

# rr_surface_raise REPO KIND SHA [DETAIL]: REPO is in a bad state of KIND at SHA.
# The first report after a clear (or ever) is the TRANSITION: it opens a record
# and surfaces it (one bead, one notification). A report while the record is
# already open is a steady state: no bead, no notification; a newer red sha is
# commented on the bead. Returns 2 on a bad argument.
rr_surface_raise() {
  local repo="$1" kind="$2" sha="$3" detail="${4:-}" rec last bead now
  rr_surface_enabled || return 0
  rr_valid_key "$repo" && rr_surface_kind_valid "$kind" && rr_valid_sha "$sha" || {
    rr_err "surface: bad arguments: $repo $kind $sha"
    return 2
  }
  now="$(rr_now)"
  rec="$(rr_surface_load "$repo" "$kind")"
  # a close that never completed is void once the state is bad again: the bead is
  # still the right one for it
  if [[ -n "$(rr_jq -r '.pendingClose.sha // ""' <<<"$rec")" ]]; then
    rec="$(rr_jq -c 'del(.pendingClose)' <<<"$rec")"
    rr_surface_store "$repo" "$kind" "$rec" || return 1
  fi
  if [[ "$(rr_jq -r '.state // ""' <<<"$rec")" != open ]]; then
    # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
    rec="$(rr_jq -nc --arg repo "$repo" --arg kind "$kind" --arg sha "$sha" --arg detail "$detail" --arg now "$now" \
      '{repo: $repo, kind: $kind, state: "open", sha: $sha, lastSha: $sha, detail: $detail,
        bead: "", notified: false, openedAt: $now, updatedAt: $now}')" || return 1
    rr_surface_store "$repo" "$kind" "$rec" || return 1
    rr_log "surface: $repo $kind: transition at $sha"
  else
    last="$(rr_jq -r '.lastSha // ""' <<<"$rec")"
    if [[ ($kind == red || $kind == build-red) && $sha != "$last" ]]; then
      # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
      rec="$(rr_jq -c --arg sha "$sha" --arg now "$now" '.lastSha = $sha | .updatedAt = $now' <<<"$rec")"
      rr_surface_store "$repo" "$kind" "$rec" || return 1
      bead="$(rr_jq -r '.bead // ""' <<<"$rec")"
      if [[ -n $bead ]]; then
        rr_bd comments add "$bead" "Still red at a newer tip: $sha. ${detail}" >/dev/null 2>&1 ||
          rr_log "surface: $repo $kind: could not comment on $bead"
      fi
    fi
  fi
  rr_surface_flush "$repo" "$kind"
}

# rr_surface_resolve REPO KIND SHA EVIDENCE: the bad state of KIND is over at SHA.
# Records the request and finishes it (comment with EVIDENCE, close the runner's
# own bead). A no-op when no transition of that kind is open.
rr_surface_resolve() {
  local repo="$1" kind="$2" sha="$3" evidence="$4" rec now
  rr_surface_enabled || return 0
  rec="$(rr_surface_load "$repo" "$kind")"
  [[ "$(rr_jq -r '.state // ""' <<<"$rec")" == open ]] || return 0
  now="$(rr_now)"
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rec="$(rr_jq -c --arg sha "$sha" --arg ev "$evidence" --arg now "$now" \
    '.pendingClose = {sha: $sha, evidence: $ev} | .updatedAt = $now' <<<"$rec")"
  rr_surface_store "$repo" "$kind" "$rec" || return 1
  rr_surface_flush "$repo" "$kind"
}

# rr_surface_green REPO REPOPATH SHA EVIDENCE: a green check verdict at SHA. It
# resolves an open red transition when SHA descends from the red tip (a later
# tip, so the red state is really over); an unrelated green leaves it open.
rr_surface_green() {
  local repo="$1" path="$2" sha="$3" evidence="$4" rec last
  rr_surface_enabled || return 0
  rec="$(rr_surface_load "$repo" red)"
  [[ "$(rr_jq -r '.state // ""' <<<"$rec")" == open ]] || return 0
  last="$(rr_jq -r '.lastSha // ""' <<<"$rec")"
  if [[ $sha != "$last" ]] && rr_git -C "$path" merge-base --is-ancestor "$last" "$sha" 2>/dev/null; then
    rr_surface_resolve "$repo" red "$sha" "$evidence"
  else
    rr_log "surface: $repo red: green at $sha does not descend from the red tip $last; left open"
  fi
}

# --- sync (runner A, report-only: F5) -------------------------------------
#
# One pass over one repo (the item): precheck the canonical clone read-only,
# fetch the primary branch's remote-tracking ref, classify behind/ahead, and
# fast-forward a behind clone. It never rebases, resets or forces: a diverged
# repo is SURFACED (one deduplicated bead, via the failure-surfacing step), and an
# ahead repo is RECORDED and handed to the push step (rr_push_step, below, behind
# ADR 0108 and `repos.<key>.push`, which nothing in this repository sets).
#
# The result of every pass is a Memento in $RR_STATE_DIR/sync/<repo>.json that
# `status` prints.

# rr_sync_store REPO JSON: atomic write (temp file, then rename).
rr_sync_store() {
  local dir="$RR_STATE_DIR/sync" tmp
  mkdir -p "$dir" || return 1
  tmp="$(mktemp "$dir/.$1.XXXXXX")" || return 1
  if ! printf '%s\n' "$2" >"$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  mv -f "$tmp" "$dir/$1.json" || {
    rm -f "$tmp"
    return 1
  }
}

# rr_sync_record REPO BRANCH OUTCOME BEHIND AHEAD PRE_BEHIND DETAIL
# BEHIND, AHEAD and PRE_BEHIND are integers or empty (unknown). PRE_BEHIND is
# the behind count read from the remote-tracking ref BEFORE the fetch: the stale
# reading the design warns about, kept next to the real one.
rr_sync_record() {
  local rec
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rec="$(rr_jq -nc --arg repo "$1" --arg branch "$2" --arg outcome "$3" \
    --arg behind "$4" --arg ahead "$5" --arg pre "$6" --arg detail "$7" --arg at "$(rr_now)" \
    '{repo: $repo, branch: $branch, outcome: $outcome,
      behind: ($behind | tonumber? // null), ahead: ($ahead | tonumber? // null),
      behindBeforeFetch: ($pre | tonumber? // null), detail: $detail, at: $at}')" || return 1
  rr_sync_store "$1" "$rec"
}

# rr_sync_records_json: every sync record as one JSON array, by repo.
rr_sync_records_json() {
  local f
  local -a files=()
  if [[ -d $RR_STATE_DIR/sync ]]; then
    while IFS= read -r -d '' f; do
      files+=("$f")
    done < <(find "$RR_STATE_DIR/sync" -type f -name '*.json' ! -name '.*' -print0 | sort -z)
  fi
  if ((${#files[@]} == 0)); then
    echo '[]'
    return 0
  fi
  rr_jq -s 'sort_by(.repo)' "${files[@]}"
}

# rr_sync_steady PATH BRANCH: the canonical-clone precheck, read-only (R-3).
# Succeeds when the clone has no core.worktree, is on BRANCH, has an empty
# `git status --porcelain` and no rebase, merge, cherry-pick or revert in
# progress. Otherwise prints the reason and returns 1. It takes no git lock and
# writes nothing (GIT_OPTIONAL_LOCKS=0: not even a refreshed index).
rr_sync_steady() {
  local path="$1" branch="$2" gitdir head porcelain m
  if rr_git -C "$path" config --local --get core.worktree >/dev/null 2>&1; then
    echo "core.worktree is set"
    return 1
  fi
  head="$(rr_git -C "$path" symbolic-ref --short -q HEAD 2>/dev/null)" || head=""
  if [[ $head != "$branch" ]]; then
    echo "HEAD is ${head:-detached}, not $branch"
    return 1
  fi
  gitdir="$(rr_git -C "$path" rev-parse --absolute-git-dir 2>/dev/null)" || {
    echo "cannot read the git directory"
    return 1
  }
  for m in rebase-merge rebase-apply MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD; do
    if [[ -e $gitdir/$m ]]; then
      echo "a $m operation is in progress"
      return 1
    fi
  done
  porcelain="$(GIT_OPTIONAL_LOCKS=0 rr_git -C "$path" status --porcelain 2>/dev/null)" || {
    echo "git status failed"
    return 1
  }
  if [[ -n $porcelain ]]; then
    echo "the working tree is not clean"
    return 1
  fi
}

# rr_sync_counts PATH BRANCH: prints "<behind> <ahead>" of refs/heads/BRANCH
# against refs/remotes/origin/BRANCH, or returns 1 when either ref is missing.
rr_sync_counts() {
  rr_git -C "$1" rev-list --left-right --count "refs/remotes/origin/$2...refs/heads/$2" 2>/dev/null
}

# rr_sync_fetch PATH BRANCH: updates the remote-tracking ref of BRANCH and
# nothing else of the clone (no working tree, no local branch). The refspec is
# explicit so that a clone without a default fetch refspec cannot silently keep
# a stale ref. Never prompts, and ends after 300 s when `timeout` is available.
rr_sync_fetch() {
  local -a pre=()
  if command -v timeout >/dev/null 2>&1; then pre=(timeout 300); fi
  GIT_TERMINAL_PROMPT=0 "${pre[@]}" env -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR \
    -u GIT_INDEX_FILE -u GIT_PREFIX -u GIT_OBJECT_DIRECTORY "${RR_GIT_BIN:-git}" \
    -C "$1" fetch --quiet --no-tags origin "+refs/heads/$2:refs/remotes/origin/$2"
}

# --- push (runner A's push step: F6, ADR 0108) -----------------------------
#
# The ONE place the runner publishes. The command is exactly
#
#   git -C <canonical clone> push origin <verified-sha>:refs/heads/<primary>
#
# (rr_push_exec): no force flag of any kind, no `+` refspec, no deletion, no other
# ref, no --tags, no --mirror, no --no-verify (the repo's pre-push hooks run), and
# never `pn workspace push|update|apply`. It runs only when every gate of ADR 0108
# decision 3 holds in the same pass (rr_push_step), and only for a repo whose
# `repos.<key>.push` is exactly true, which no configuration in this repository sets.
# The first pass after enabling a repo is a dry run (decision 6): it logs the range it
# WOULD push and pushes nothing, until the operator drops the flag file
# <state dir>/push-live/<repo> (or sets `repos.<key>.dry_run = false`).

# rr_push_live REPO: the dry-run mode was removed for REPO (flag file, or the
# config option exactly false). Anything else is dry-run: the default.
rr_push_live() {
  [[ -e $RR_STATE_DIR/push-live/$1 ]] && return 0
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rr_jq -e --arg k "$1" '.repos[$k].dry_run == false' <<<"$RR_CONFIG_JSON" >/dev/null 2>&1
}

# rr_verdict_green REPO SHA: a check verdict for exactly SHA, green, over the locked
# inputs (what a fresh clone sees). A verdict of another sha never matches.
rr_verdict_green() {
  local f="$RR_STATE_DIR/verdicts/$1/$2.check.json"
  [[ -f $f ]] || return 1
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rr_jq -e --arg sha "$2" '.sha == $sha and .kind == "check" and .result == "green" and .inputs == "locked"' "$f" >/dev/null 2>&1
}

# rr_push_exec PATH SHA BRANCH: THE push. Never prompts, ends after 300 s when
# `timeout` is available. Nothing else in this code base runs `git push`.
rr_push_exec() {
  local -a pre=()
  if command -v timeout >/dev/null 2>&1; then pre=(timeout 300); fi
  GIT_TERMINAL_PROMPT=0 "${pre[@]}" env -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR \
    -u GIT_INDEX_FILE -u GIT_PREFIX -u GIT_OBJECT_DIRECTORY "${RR_GIT_BIN:-git}" \
    -C "$1" push origin "$2:refs/heads/$3"
}

# rr_push_siblings_ahead REPO: prints the consumed siblings (repos.<key>.consumes)
# whose canonical primary branch holds commits their remote lacks, one per line.
# Read-only; RR_REPO_PATH is preserved.
rr_push_siblings_ahead() {
  local repo="$1" root sib saved="${RR_REPO_PATH:-}" br
  root="$(rr_cfg '.workspace_root // empty')"
  while IFS= read -r sib; do
    [[ -n $sib ]] && rr_valid_key "$sib" || continue
    rr_path_check "$root" "$sib" || continue
    br="$(rr_repo_branch "$root" "$sib")" || continue
    if (($(rr_git -C "$RR_REPO_PATH" rev-list --count "refs/remotes/origin/$br..refs/heads/$br" 2>/dev/null || echo 0) > 0)); then
      printf '%s\n' "$sib"
    fi
  done < <(rr_jq -r --arg k "$repo" '(.repos[$k].consumes // [])[]' <<<"$RR_CONFIG_JSON")
  RR_REPO_PATH="$saved"
}

# rr_push_withheld REPO BRANCH AHEAD PRE REASON: the push did not happen; the sync
# record stays `ahead` and says why.
rr_push_withheld() {
  rr_sync_record "$1" "$2" ahead 0 "$3" "$4" "push withheld: $5" || return 1
  rr_log "sync: $1 is $3 commit(s) ahead of origin/$2; push withheld: $5"
}

# rr_push_audit REPO PATH BRANCH OLD NEW VERDICT_FILE: ADR 0108 decision 7. One
# record (repo, remote, old and new remote sha, verdict reference, time) appended to
# $RR_STATE_DIR/push-audit.jsonl, written to the run log and to stderr as one
# structured line.
rr_push_audit() {
  local repo="$1" path="$2" branch="$3" old="$4" new="$5" vf="$6" remote rec
  remote="$(rr_git -C "$path" remote get-url origin 2>/dev/null | sed -E 's|//[^/@]*@|//|')"
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  rec="$(rr_jq -nc --arg repo "$repo" --arg remote "$remote" --arg branch "$branch" \
    --arg old "$old" --arg new "$new" --arg at "$(rr_now)" --arg vf "$vf" \
    --slurpfile v "$vf" \
    '{event: "push", repo: $repo, remote: $remote, branch: $branch, oldRemoteSha: $old,
      newRemoteSha: $new, verdict: {path: $vf, sha: $v[0].sha, kind: $v[0].kind,
      result: $v[0].result, finishedAt: $v[0].finishedAt}, at: $at}')" || return 1
  printf '%s\n' "$rec" >>"$RR_STATE_DIR/push-audit.jsonl" || return 1
  rr_log "push-audit $rec"
  printf 'pg-repo-runner: push-audit %s\n' "$rec" >&2
}

# rr_push_step REPO PATH BRANCH TIP AHEAD PRE_BEHIND: the push candidate of a sync pass (the
# repo is ahead only, the canonical precheck passed earlier in this same pass). Gates
# of ADR 0108 decision 3, then dry-run, then the push. Returns 1 only when its own
# record cannot be written; a withheld, refused or dry-run push is a normal exit.
rr_push_step() {
  local repo="$1" path="$2" branch="$3" tip="$4" ahead="$5" pre_behind="$6"
  local vf kill old sibs cur out rc count

  # (a) authorization: push is exactly true, and no kill switch
  if ! rr_repo_flag "$repo" push; then
    rr_sync_record "$repo" "$branch" ahead 0 "$ahead" "$pre_behind" "" || return 1
    rr_log "sync: $repo is $ahead commit(s) ahead of origin/$branch; nothing to do"
    return 0
  fi
  if kill="$(rr_kill_switch_active)"; then
    rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "kill switch present: $kill"
    return
  fi

  # (b) a green check verdict for exactly the tip, which is the current primary tip
  vf="$RR_STATE_DIR/verdicts/$repo/$tip.check.json"
  if ! rr_verdict_green "$repo" "$tip"; then
    if [[ -f $vf ]] && [[ "$(rr_jq -r '.result' "$vf" 2>/dev/null)" == red ]]; then
      sibs="$(rr_push_siblings_ahead "$repo" | paste -sd, -)"
      if [[ -n $sibs ]]; then
        rr_surface_raise "$repo" needs-relock "$tip" \
          "red under locked inputs while consumed sibling(s) $sibs have unpublished commits: run pn workspace push" ||
          rr_log "surface: raising the needs-relock transition failed for $repo"
        rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "blocked, needs sibling push and relock (siblings: $sibs): run pn workspace push"
        return
      fi
      rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "the check verdict for $tip is red"
      return
    fi
    rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "no green check verdict for the tip $tip"
    return
  fi
  cur="$(rr_tip_of "$path" "$branch")" || cur=""
  if [[ $cur != "$tip" ]]; then
    rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "the tip moved to $cur"
    return
  fi

  # (d) origin/<primary> is an ancestor of the verified sha (the push is a fast-forward)
  old="$(rr_git -C "$path" rev-parse --verify -q "refs/remotes/origin/$branch^{commit}")" || old=""
  if [[ -z $old ]] || ! rr_git -C "$path" merge-base --is-ancestor "$old" "$tip"; then
    rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "origin/$branch is not an ancestor of $tip"
    return
  fi

  # the gates are re-read once more right before the act: the tip and the kill
  # switch may have changed while the ancestry was checked
  cur="$(rr_tip_of "$path" "$branch")" || cur=""
  if [[ $cur != "$tip" ]]; then
    rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "the tip moved to $cur"
    return
  fi
  if kill="$(rr_kill_switch_active)"; then
    rr_push_withheld "$repo" "$branch" "$ahead" "$pre_behind" "kill switch present: $kill"
    return
  fi
  count="$(rr_git -C "$path" rev-list --count "$old..$tip" 2>/dev/null || echo "$ahead")"

  # dry run (decision 6): the default
  if ! rr_push_live "$repo"; then
    rr_sync_record "$repo" "$branch" push-dry-run 0 "$ahead" "$pre_behind" \
      "dry run: would push $old..$tip ($count commit(s)) to origin $branch" || return 1
    rr_log "sync: $repo dry run: would push $old..$tip ($count commit(s)) to origin refs/heads/$branch; nothing pushed"
    rr_git -C "$path" log --oneline --max-count=50 "$old..$tip" 2>/dev/null |
      while IFS= read -r out; do rr_log "sync: $repo dry run: would push $out"; done
    return 0
  fi

  # the push
  rc=0
  out="$(rr_push_exec "$path" "$tip" "$branch" 2>&1)" || rc=$?
  if ((rc != 0)); then
    rr_sync_record "$repo" "$branch" push-rejected 0 "$ahead" "$pre_behind" \
      "git push exited $rc; not retried this pass: $(tail -n 1 <<<"$out")" || return 1
    rr_log "sync: $repo push refused (exit $rc); not forced, not retried this pass: $(tail -n 1 <<<"$out")"
    return 0
  fi
  cur="$(rr_git -C "$path" rev-parse --verify -q "refs/remotes/origin/$branch^{commit}")" || cur="$tip"
  rr_sync_record "$repo" "$branch" pushed 0 "$ahead" "$pre_behind" "pushed $old..$cur" || return 1
  rr_push_audit "$repo" "$path" "$branch" "$old" "$cur" "$vf" ||
    rr_log "sync: $repo: writing the push audit record failed"
  rr_surface_resolve "$repo" needs-relock "$tip" "Pushed $tip to origin $branch: no longer blocked." ||
    rr_log "surface: resolving the needs-relock transition failed for $repo"
}

# --- workspace build (runner C: F8) ----------------------------------------
#
# One build of the WHOLE workspace at one tuple of primary tips, in a throwaway
# coordinated workforest set (`pn workspace workforest add ... --no-hooks`), never in
# a canonical working tree. The tuple is every repo key of the canonical
# pn-workspace.toml: the set holds all of them, so every one MUST be on the allow-list
# with `build = true` (else the run is refused and nothing is created). The tuple's
# identity is `workspace@<sha256 of the sorted "repo=sha" lines>`.
#
# The prefix `runner-build-` of a set's branch is RESERVED for this runner: any set or
# branch carrying it that is older than the current lease owner's start is a leftover
# of a crashed run and is removed first. Nothing else is ever removed.

RR_WS_PREFIX="runner-build-"

# rr_pn ARGS...: pn with the repository-location variables unset (a hook or worktree
# environment would otherwise steer the git calls pn makes). PG_REPO_RUNNER_PN_BIN
# replaces the binary (a test seam); pn is an ambient runtime dependency, like bd.
rr_pn() {
  env -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR -u GIT_INDEX_FILE \
    -u GIT_PREFIX -u GIT_OBJECT_DIRECTORY "${RR_PN_BIN:-pn}" "$@"
}

# rr_quiet_window: how long the tip tuple must stay unchanged before it is offered, in
# seconds (PG_REPO_RUNNER_QUIET_WINDOW, else thresholds.quiet_window, else 900: 3 ticks
# of 5 minutes). 0 is allowed.
rr_quiet_window() {
  local v="${PG_REPO_RUNNER_QUIET_WINDOW:-}"
  [[ -n $v ]] || v="$(rr_cfg '.thresholds.quiet_window // empty')"
  [[ $v =~ ^[0-9]+$ ]] || v=900
  printf '%s' "$v"
}

# rr_ws_collect ROOT: reads the workspace tip tuple. Sets RR_WS_TIPS (sorted
# "repo=sha" lines), RR_WS_BRANCH (the one primary branch every repo shares),
# RR_WS_PATHS (the canonical path of every repo) and RR_WS_HASH. Returns 1 with the
# reason in RR_WS_WHY when the tuple cannot be a build input: a repo that is not on the
# allow-list with build = true, one that fails the path check, one with no primary tip,
# or primary branches that differ (workforest add takes ONE start point for all repos).
# It reads refs only. Call it without a subshell.
# shellcheck disable=SC2034  # the RR_WS_* results are read by the source and the role
rr_ws_collect() {
  local root="$1" key branch tip keys="" b0=""
  RR_WS_TIPS="" RR_WS_BRANCH="" RR_WS_WHY="" RR_WS_HASH=""
  RR_WS_PATHS=()
  local -a lines=()
  if [[ ! -r $root/pn-workspace.toml ]] || ! keys="$(rr_tq "$root/pn-workspace.toml" 2>/dev/null | rr_jq -r '(.repos // {}) | keys[]')"; then
    RR_WS_WHY="cannot read $root/pn-workspace.toml"
    return 1
  fi
  if [[ -z $keys ]]; then
    RR_WS_WHY="$root/pn-workspace.toml names no repo"
    return 1
  fi
  while IFS= read -r key; do
    if ! rr_valid_key "$key"; then
      RR_WS_WHY="'$key' is not a plain repo key"
      return 1
    fi
    if ! rr_repo_listed "$key"; then
      RR_WS_WHY="repo '$key' is not in the allow-list"
      return 1
    fi
    if ! rr_repo_flag "$key" build; then
      RR_WS_WHY="build is not enabled for repo '$key' (the set holds every workspace repo, so every one must opt in)"
      return 1
    fi
    if ! rr_path_check "$root" "$key"; then
      RR_WS_WHY="$root/$key is not the top level of a git repository at that exact real path"
      return 1
    fi
    branch="$(rr_repo_branch "$root" "$key")" || {
      RR_WS_WHY="cannot read the primary branch of '$key'"
      return 1
    }
    if [[ -z $b0 ]]; then
      b0="$branch"
    elif [[ $branch != "$b0" ]]; then
      RR_WS_WHY="primary branches differ ('$key' has $branch, others $b0): one set start point cannot serve them"
      return 1
    fi
    if [[ $b0 == -* ]] || ! rr_git check-ref-format "refs/heads/$b0"; then
      RR_WS_WHY="the primary branch name is not usable: $b0"
      return 1
    fi
    tip="$(rr_tip_of "$RR_REPO_PATH" "$branch")" || {
      RR_WS_WHY="refs/heads/$branch of '$key' does not exist"
      return 1
    }
    lines+=("$key=$tip")
    RR_WS_PATHS+=("$RR_REPO_PATH")
  done <<<"$keys"
  RR_WS_TIPS="$(printf '%s\n' "${lines[@]}" | LC_ALL=C sort)"
  RR_WS_BRANCH="$b0"
  RR_WS_HASH="$(printf '%s\n' "$RR_WS_TIPS" | sha256sum | cut -d' ' -f1)"
}

# rr_ws_tips_json TIPS: the "repo=sha" lines as one JSON object {repo: sha}.
rr_ws_tips_json() {
  # shellcheck disable=SC2016  # jq program, not shell: $-names are jq variables
  printf '%s\n' "$1" | rr_jq -Rn '[inputs | select(length > 0) | split("=") | {(.[0]): .[1]}] | add // {}'
}

# rr_ws_set_name EPOCH: the throwaway set's branch name for a run that started at EPOCH.
rr_ws_set_name() {
  printf '%s%s' "$RR_WS_PREFIX" "$(date -u -d "@$1" +%Y%m%dT%H%M%SZ)"
}

# rr_ws_name_epoch NAME: the epoch a set name encodes; fails for any other shape.
rr_ws_name_epoch() {
  local n="${1#"$RR_WS_PREFIX"}"
  [[ $n =~ ^([0-9]{4})([0-9]{2})([0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})Z$ ]] || return 1
  date -u -d "${BASH_REMATCH[1]}-${BASH_REMATCH[2]}-${BASH_REMATCH[3]}T${BASH_REMATCH[4]}:${BASH_REMATCH[5]}:${BASH_REMATCH[6]}Z" +%s 2>/dev/null
}

# rr_ws_set_name_ok NAME: a reserved-prefix name made of safe characters only.
rr_ws_set_name_ok() {
  [[ $1 =~ ^runner-build-[A-Za-z0-9._-]+$ && $1 != *..* ]]
}

# rr_ws_leftovers ROOT: the names of every set directory and every member-repo branch
# that carries the reserved prefix (RR_WS_PATHS is set by rr_ws_collect), one per line,
# without duplicates.
rr_ws_leftovers() {
  local root="$1" f p
  {
    if [[ -d $root/.workforests ]]; then
      while IFS= read -r -d '' f; do
        basename "$f"
      done < <(find "$root/.workforests" -mindepth 1 -maxdepth 1 -name "${RR_WS_PREFIX}*" -print0)
    fi
    for p in "${RR_WS_PATHS[@]}"; do
      rr_git -C "$p" for-each-ref --format='%(refname:short)' "refs/heads/${RR_WS_PREFIX}*" 2>/dev/null
    done
  } | LC_ALL=C sort -u
}

# rr_ws_remove_set ROOT NAME: removes the runner's own throwaway set NAME and its
# branches: `pn workspace workforest remove NAME --force`, a prune of the stale
# worktree entries (a set directory that vanished leaves them behind, and a branch with
# a worktree entry cannot be deleted), then `git branch -D NAME` in every member repo.
# Only a reserved-prefix name is ever touched. Returns 1 when something is left.
rr_ws_remove_set() {
  local root="$1" name="$2" setdir p rc=0 log
  rr_ws_set_name_ok "$name" || {
    rr_err "refusing to remove '$name': not a reserved runner-build- set name"
    return 2
  }
  mkdir -p "$RR_STATE_DIR/logs/workspace" || return 1
  log="$RR_STATE_DIR/logs/workspace/set-cleanup.log"
  setdir="$root/.workforests/$name"
  if [[ -e $setdir ]]; then
    PN_WORKSPACE_ROOT="$root" rr_pn workspace workforest remove "$name" --force >>"$log" 2>&1 ||
      rr_log "workspace: removing the set $name failed (see $log)"
  fi
  PN_WORKSPACE_ROOT="$root" rr_pn workspace workforest prune >>"$log" 2>&1 || true
  for p in "${RR_WS_PATHS[@]}"; do
    if rr_git -C "$p" show-ref --verify -q "refs/heads/$name"; then
      rr_git -C "$p" branch -D "$name" >>"$log" 2>&1 || {
        rr_log "workspace: deleting branch $name in $p failed (see $log)"
        rc=1
      }
    fi
  done
  [[ ! -e $setdir ]] || rc=1
  return "$rc"
}

# rr_ws_remove_leftovers ROOT START_EPOCH: removes every leftover set older than the
# run that holds the lease (a set name that encodes a time at or before START_EPOCH, or
# no readable time at all: the prefix is reserved). A set that claims to be newer than
# this run is left alone and logged: it cannot be a crashed predecessor. Returns 1 when
# a leftover could not be removed.
rr_ws_remove_leftovers() {
  local root="$1" start="$2" name e rc=0
  while IFS= read -r name; do
    [[ -n $name ]] || continue
    rr_ws_set_name_ok "$name" || {
      rr_log "workspace: leftover '$name' has an unsafe name; left alone"
      continue
    }
    if e="$(rr_ws_name_epoch "$name")" && ((e > start)); then
      rr_log "workspace: $name is newer than this run (start $start); left alone"
      continue
    fi
    rr_log "workspace: removing leftover set $name"
    rr_ws_remove_set "$root" "$name" || rc=1
  done < <(rr_ws_leftovers "$root")
  return "$rc"
}
