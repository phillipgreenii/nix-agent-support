#!/usr/bin/env bats
# bats file_tags=type:unit
#
# The strict-PATH guard: pg-router runs pg-repo-runner under a base PATH of only
# /usr/bin:/bin:/usr/sbin:/sbin (BSD tools, no GNU coreutils). Two bugs of that class
# shipped (GNU `date -d`, GNU `realpath -e`) and made `tips` skip every repo while
# exiting 0. Each test here runs a role with a PATH that holds ONLY
#   - a whitelist of tools the audit proved BSD-safe with the flags the kernel uses
#     (cat chmod cut dirname basename env find head id ln mkdir mktemp mv paste
#     readlink rm sed sort tail tr, plus sh and bash for shebangs), and
# the injected tool dir (RR_TOOLS_PATH, what default.nix supplies as the store GNU
# toolset: sha256sum nproc date timeout nice ps). Any other bare tool is "command not
# found", so a new GNU-only (or merely unlisted) call fails the test instead of passing
# on the developer's full PATH. The raw script is run on purpose (never the built one,
# whose wrapper appends a fallback PATH that would hide a missing tool).
#
# To add a bare tool: prove it BSD-safe and add it to STRICT_TOOLS, or inject it.

bats_require_minimum_version 1.5.0

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi

STRICT_TOOLS=(sh bash cat chmod cut dirname basename env find head id ln mkdir mktemp mv paste readlink rm sed sort tail tr)
INJECTED_TOOLS=(sha256sum nproc date timeout nice ps)

setup() {
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # resolve the host tools BEFORE gfh_setup rebuilds the environment
  local t p
  declare -gA HOST_TOOL=()
  for t in "${STRICT_TOOLS[@]}" "${INJECTED_TOOLS[@]}" git jq tq; do
    p="$(command -v "$t")" || {
      echo "host tool '$t' is not on PATH" >&2
      return 1
    }
    HOST_TOOL[$t]="$p"
  done
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR TEST_SUPPORT GFH_LIB
  gfh_setup "pg-repo-runner"
  gfh_restore_env
  # no test depends on a pg-origin-guard installed on the machine running it
  export PG_REPO_RUNNER_ORIGIN_GUARD_BIN=/usr/bin/true

  TEST_DIR="$GFH_WORK"
  STRICT_BIN="$TEST_DIR/strict-bin"
  INJECTED_BIN="$TEST_DIR/injected-bin"
  mkdir -p "$STRICT_BIN" "$INJECTED_BIN" "$TEST_DIR/remotes"
  for t in "${STRICT_TOOLS[@]}"; do ln -s "${HOST_TOOL[$t]}" "$STRICT_BIN/$t"; done
  for t in "${INJECTED_TOOLS[@]}"; do ln -s "${HOST_TOOL[$t]}" "$INJECTED_BIN/$t"; done

  WS="$TEST_DIR/ws"
  mkdir -p "$WS"
  make_repo "$WS/nix-alpha" alpha
  make_repo "$WS/nix-beta" beta
  printf "[workspace]\nid = 'fixture'\n\n[repos]\n[repos.nix-alpha]\nurl = 'x'\nbranch = 'main'\n[repos.nix-beta]\nurl = 'x'\nbranch = 'main'\n" >"$WS/pn-workspace.toml"
  # max_load is deliberately absent: the load check then reaches rr_ncpu (nproc)
  cat >"$TEST_DIR/config.toml" <<EOF
workspace_root = "$WS"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
nice_level = 10
quiet_window = 900

[repos.nix-alpha]
verify = true
sync = true
build = true
push = false

[repos.nix-beta]
verify = false
sync = true
build = true
push = false
EOF
  STATE="$TEST_DIR/state/pg-repo-runner"
}

teardown() {
  [[ -z ${RR_DEBUG:-} ]] || cat "$STATE/run.log" >&3
  gfh_teardown
}

make_repo() {
  local dir="$1" name="$2"
  gfh_init_bare "$TEST_DIR/remotes/$name.git"
  gfh_clone "$TEST_DIR/remotes/$name.git" "$dir" "pg-repo-runner"
  echo "$name" >"$dir/f"
  git -C "$dir" add f
  git -C "$dir" commit -q -m "seed $name"
  git -C "$dir" push -q origin main
}

# strict ROLE [ARGS...]: the raw runner under the strict PATH only. The environment is
# rebuilt from scratch (env -i), so nothing of the host PATH leaks in.
strict() {
  run --separate-stderr env -i \
    PATH="$STRICT_BIN" HOME="$HOME" \
    GIT_CONFIG_GLOBAL="${GIT_CONFIG_GLOBAL:-/dev/null}" GIT_CONFIG_SYSTEM="${GIT_CONFIG_SYSTEM:-/dev/null}" \
    XDG_STATE_HOME="$TEST_DIR/state" PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml" \
    PG_REPO_RUNNER_LOADAVG=0.50 PG_REPO_RUNNER_EPOCH="${EPOCH:-1000000}" \
    PG_ROUTER_ITEM_ID="${ITEM:-}" \
    RR_GIT_BIN="${HOST_TOOL[git]}" RR_JQ_BIN="${HOST_TOOL[jq]}" RR_TQ_BIN="${HOST_TOOL[tq]}" \
    RR_DATE_BIN="$INJECTED_BIN/date" RR_TIMEOUT_BIN="$INJECTED_BIN/timeout" RR_NICE_BIN="$INJECTED_BIN/nice" \
    RR_TOOLS_PATH="${TOOLS_PATH-$INJECTED_BIN}" \
    "${HOST_TOOL[bash]}" -euo pipefail "$SCRIPTS_DIR/pg-repo-runner.sh" "$@"
}

assert_no_missing_tool() {
  ! grep -qi 'command not found\|not found' <<<"$stderr"
  [[ ! -f $STATE/run.log ]] || ! grep -qi 'command not found' "$STATE/run.log"
}

@test "strict PATH: tips emits the verify-enabled repo's tip" {
  strict tips
  [ "$status" -eq 0 ]
  assert_no_missing_tool
  [ "$(wc -l <<<"$output" | tr -d ' ')" -eq 1 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$(git -C "$WS/nix-alpha" rev-parse main)" ]
}

@test "strict PATH: workspace-tips computes the tip hash (sha256sum)" {
  strict workspace-tips
  [ "$status" -eq 0 ]
  assert_no_missing_tool
  [ -f "$STATE/workspace-tips.json" ]
  [[ "$(jq -r .hash "$STATE/workspace-tips.json")" =~ ^[0-9a-f]{64}$ ]]
}

@test "strict PATH: sync fast-forwards a behind clone and reaches the core count (nproc)" {
  local other="$TEST_DIR/other-alpha"
  gfh_clone "$TEST_DIR/remotes/alpha.git" "$other" "pg-repo-runner"
  echo more >>"$other/f"
  git -C "$other" add f
  git -C "$other" commit -q -m more
  git -C "$other" push -q origin main
  ITEM=nix-alpha strict sync
  [ "$status" -eq 0 ]
  assert_no_missing_tool
  [ "$(git -C "$WS/nix-alpha" rev-parse HEAD)" = "$(git -C "$other" rev-parse HEAD)" ]
}

@test "strict PATH negative control: without the injected tool dir sha256sum is unreachable and the test notices" {
  TOOLS_PATH="" strict workspace-tips
  # the tuple hash cannot be computed: no debounce state with a real hash is written
  if [[ -f $STATE/workspace-tips.json ]]; then
    ! [[ "$(jq -r .hash "$STATE/workspace-tips.json")" =~ ^[0-9a-f]{64}$ ]]
  fi
}
