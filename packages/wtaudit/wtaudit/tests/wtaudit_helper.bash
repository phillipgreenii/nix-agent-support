# shellcheck shell=bash
# Shared fixture helpers for the wtaudit bats suites: a hermetic git fixture
# (the shared harness, GFH_LIB), a real throwaway embedded bd database
# (bd_fixture.bash), a strict-mode runner that mimics the built script, and the
# small git/bd verbs the scenarios use. Every fixture repo lives under $FX,
# which is inside the bd database directory so `bd -C <repo>` resolves to the
# throwaway database and never to the real tracker.

bats_require_minimum_version 1.5.0

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi
# shellcheck disable=SC1091  # resolved at runtime by bats
load "$TEST_SUPPORT/bd_fixture"

# wtaudit_file_setup: call from setup_file.
wtaudit_file_setup() {
  command -v bd >/dev/null 2>&1 || skip "bd not on PATH"
  command -v lsof >/dev/null 2>&1 || skip "lsof not on PATH"
  bd_fixture_setup_file
  # A small pool of beads in each state, created ONCE per file (a bd call costs
  # about a second): the suites only read them, never mutate them.
  bd_fixture_assert_isolated || return 1
  local id
  id="$(bd -C "$TDIR" create "pool open" -p 2 --json | jq -r '.id')"
  printf '%s\n' "$id" >"$TDIR/pool.open"
  id="$(bd -C "$TDIR" create "pool closed" -p 2 --json | jq -r '.id')"
  bd -C "$TDIR" close "$id" >/dev/null 2>&1
  printf '%s\n' "$id" >"$TDIR/pool.closed"
  id="$(bd -C "$TDIR" create "pool claimed" -p 2 --json | jq -r '.id')"
  bd -C "$TDIR" update "$id" --claim --actor wtaudit-test >/dev/null 2>&1
  printf '%s\n' "$id" >"$TDIR/pool.claimed"
  id="$(bd -C "$TDIR" create "pool stranded" -p 2 --json | jq -r '.id')"
  bd -C "$TDIR" update "$id" --assignee someone >/dev/null 2>&1
  printf '%s\n' "$id" >"$TDIR/pool.stranded"
  id="$(bd -C "$TDIR" create "pool closed but assigned" -p 2 --json | jq -r '.id')"
  bd -C "$TDIR" update "$id" --claim --actor wtaudit-test >/dev/null 2>&1
  bd -C "$TDIR" close "$id" --actor wtaudit-test >/dev/null 2>&1
  printf '%s\n' "$id" >"$TDIR/pool.closed_assigned"
}

# pool_bead <open|closed|claimed|stranded|closed_assigned>: the pooled bead id.
pool_bead() { cat "$TDIR/pool.$1"; }

wtaudit_file_teardown() {
  bd_fixture_teardown_file
}

# wtaudit_test_setup: call from setup. Hermetic git env, then the bd env, then
# the per-test fixture directory $FX and the strict-mode runner $WTAUDIT.
wtaudit_test_setup() {
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090
  source "$gfh_lib"
  gfh_save_env TDIR SCRIPTS_DIR TEST_SUPPORT LIB_PATH WTDONE_BIN
  gfh_setup "wtaudit"
  gfh_restore_env
  export GFH_LIB="$gfh_lib"
  bd_fixture_setup
  FX="$TDIR/t${BATS_TEST_NUMBER}"
  mkdir -p "$FX/wts" "$FX/bin"
  FX="$(cd "$FX" && pwd -P)"
  export GIT_CEILING_DIRECTORIES="$FX"
  # shellcheck disable=SC2034  # read by the suites' teardown and anchor helpers
  ANCHOR_PID=""
  cd "$FX" || return 1

  # The liveness library: the nix check exports LIB_PATH (the composed file);
  # a source checkout uses the sibling package.
  local live
  if [[ -n ${LIB_PATH:-} ]]; then
    if [[ -d $LIB_PATH ]]; then live="$LIB_PATH/wt-liveness-lib.bash"; else live="${LIB_PATH%%:*}"; fi
  else
    live="$SCRIPTS_DIR/../../wtdone/wt-liveness-lib/wt-liveness-lib.bash"
  fi
  WT_LIVENESS_LIB="$live"

  # A strict-mode runner (the builder injects `set -euo pipefail`): sources the
  # library, the logic file and the entry point exactly as the built script.
  cat >"$FX/bin/wtaudit" <<RUNNER
#!/usr/bin/env bash
set -euo pipefail
source "$live"
source "$SCRIPTS_DIR/wtaudit.bash"
source "$SCRIPTS_DIR/wtaudit.sh"
RUNNER
  chmod +x "$FX/bin/wtaudit"
  WTAUDIT="$FX/bin/wtaudit"

  # The real wtdone, pinned through WTDONE_BIN as the nix package does. The
  # nix check already exports WTDONE_BIN (the store path); a source checkout
  # wraps the sibling script.
  if [[ -z ${WTDONE_BIN:-} ]]; then
    cat >"$FX/bin/wtdone-real" <<WTDONE
#!/usr/bin/env bash
export LIB_PATH="$live"
exec bash "$SCRIPTS_DIR/../../wtdone/wtdone/wtdone.sh" "\$@"
WTDONE
    chmod +x "$FX/bin/wtdone-real"
    WTDONE_BIN="$FX/bin/wtdone-real"
  fi
  export WTDONE_BIN
}

wtaudit_test_teardown() {
  if [[ -n ${ANCHOR_PID:-} ]]; then
    kill "$ANCHOR_PID" 2>/dev/null || true
    wait "$ANCHOR_PID" 2>/dev/null || true
  fi
  gfh_teardown
}

# source_lib: load the library functions into the test shell (lib suite).
source_lib() {
  # shellcheck disable=SC1090
  source "$WT_LIVENESS_LIB"
  # shellcheck disable=SC1091
  source "$SCRIPTS_DIR/wtaudit.bash"
}

# ---- git fixture verbs ------------------------------------------------------

# mk_repo <name>: a repo $FX/<name> on main with one commit.
mk_repo() {
  gfh_init_repo "$FX/$1" wtaudit
  git -C "$FX/$1" commit -q --allow-empty -m "root"
  printf '%s\n' "$FX/$1"
}

# commit_in <dir> <file> <message>: write <file> (unique content) and commit.
commit_in() {
  local dir="$1" file="$2" msg="$3"
  printf '%s %s\n' "$msg" "$RANDOM$RANDOM" >>"$dir/$file"
  git -C "$dir" add "$file"
  git -C "$dir" commit -q -m "$msg"
}

# add_wt <repo> <branch> [<dir>]: a worktree on a NEW branch outside the repo.
add_wt() {
  local repo="$1" branch="$2" dir="${3:-$FX/wts/${2//\//_}}"
  git -C "$repo" worktree add -q -b "$branch" "$dir" >/dev/null
  printf '%s\n' "$dir"
}

# ---- bd verbs (the real throwaway database) ---------------------------------

# ---- bd verbs: see pool_bead (created once per file) -----------------------

# ---- running the tool -------------------------------------------------------

# classify <repo> <branch>: the scan row of <branch> in <repo> (JSON), using
# the library function directly.
classify() {
  wtaudit_scan_repo "$(basename "$1")" "$1" | jq -c --arg b "$2" 'select(.branch == $b)'
}

# verdict <repo> <branch>: "<action> <reason>".
verdict() {
  classify "$1" "$2" | jq -r '"\(.action) \(.reason)"'
}

# cli_scan <repo>...: run `wtaudit scan` over the repos; stdout is the JSON.
cli_scan() {
  local args=() r
  for r in "$@"; do args+=(--repo "$r"); done
  "$WTAUDIT" scan "${args[@]}"
}

# row_of <scan-json-file-or-var> <branch>: see callers; uses jq on $SCAN.
row() {
  jq -c --arg b "$1" '.candidates[] | select(.branch == $b)' <<<"$SCAN"
}

# ---- processes --------------------------------------------------------------

# start_anchor <dir>: a real `bash` whose cwd is <dir> (on the allow-list) with
# a real `sleep` child (not on it). Waits until lsof sees both.
start_anchor() {
  bash -c 'trap "kill \$child 2>/dev/null; exit 0" TERM; cd "$1" || exit 1; sleep 60 & child=$!; wait' _ "$1" 3>&- &
  ANCHOR_PID=$!
  local _ n
  for _ in $(seq 1 25); do
    n="$(lsof -a -d cwd +D "$1" -F p 2>/dev/null | grep -c '^p' || true)"
    [[ $n -ge 2 ]] && break
    sleep 0.2
  done
}

stop_anchor() {
  if [[ -n ${ANCHOR_PID:-} ]]; then
    kill "$ANCHOR_PID" 2>/dev/null || true
    wait "$ANCHOR_PID" 2>/dev/null || true
    ANCHOR_PID=""
  fi
}

# mock_bd <body-script>: put a `bd` on PATH that runs <body-script>.
mock_bd() {
  mkdir -p "$FX/mockbin"
  printf '#!/usr/bin/env bash\n%s\n' "$1" >"$FX/mockbin/bd"
  chmod +x "$FX/mockbin/bd"
  PATH="$FX/mockbin:$PATH"
  export PATH
}

# mock_bd_bead <status> <assignee> <lease> [array|data]: a bd that answers any
# `show <id> --json` with a bead whose id is the requested id, in the bare-array
# or the {schema_version,data} envelope.
mock_bd_bead() {
  local status="$1" assignee="$2" lease="$3" envelope="${4:-array}"
  local obj body
  obj="{\"id\":\"__ID__\",\"title\":\"mock\",\"status\":\"$status\",\"assignee\":\"$assignee\",\"lease_expires_at\":\"$lease\",\"labels\":[\"x\"]}"
  case "$envelope" in
  array) body="[$obj]" ;;
  data) body="{\"schema_version\":1,\"data\":[$obj]}" ;;
  *) return 1 ;;
  esac
  mock_bd "out='$body'; printf '%s\\n' \"\${out//__ID__/\$4}\""
}
