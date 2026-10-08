#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests for pg-rescue-flake-lock-conflict (bead pg2-3ybxg): real
# git against scratch repositories, a fake `nix` that writes a deterministic
# lock (no network), and a stub `pg-rescue` that records its `result` call.
# The wrapper-driven tests (the REAL pg-rescue running this handler) live in
# packages/pg-rescue/cmd/pg-rescue/flakelock_handler_test.go.

setup() {
  # Capture what the suite needs across gfh_setup: gfh_reset_env rebuilds the
  # exported environment from an allowlist, so SCRIPTS_DIR / SCRIPT_UNDER_TEST /
  # TEST_SUPPORT (injected by the nix check) and GFH_LIB (local runs) MUST be
  # saved BEFORE it and restored AFTER it
  # (code-file-standards "Tests That Need Git"; pg2-emjgm).
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  # GFH_LIB is the one required file (tc-4ehow); the nix check injects
  # TEST_SUPPORT (the directory holding it) instead, so alias that.
  if [[ -z ${GFH_LIB:-} && -n ${TEST_SUPPORT:-} ]]; then
    GFH_LIB="$TEST_SUPPORT/git-fixture-harness.bash"
  fi
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB

  # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
  # allowlist reset + fresh HOME + hooks disabled + per-suite identity): a
  # `git commit` from a linked worktree exports GIT_DIR and friends into the
  # pre-commit hook, and this suite inherits them from the run-unit-tests hook.
  gfh_setup "pg-rescue-flake-lock-conflict"
  gfh_restore_env

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    HANDLER=("$SCRIPT_UNDER_TEST")
  else
    HANDLER=(bash -euo pipefail "$SCRIPTS_DIR/pg-rescue-flake-lock-conflict.sh")
  fi

  TEST_DIR="$GFH_WORK"

  # Fakes live OUTSIDE the repository under test.
  STUB_DIR="$TEST_DIR/bin"
  mkdir -p "$STUB_DIR"
  export PATH="$STUB_DIR:$PATH"
  export NIX_LOG="$TEST_DIR/nix.log" RESULT_LOG="$TEST_DIR/result.log"
  cat >"$STUB_DIR/nix" <<'NIX'
#!/bin/sh
echo "$*" >>"$NIX_LOG"
[ "$1 $2" = "flake update" ] || { echo "fake nix: unsupported: $*" >&2; exit 64; }
shift 2
printf 'relocked: %s\n' "$*" >flake.lock
NIX
  # Stub `pg-rescue result OUTCOME SUMMARY [--details-file F]`: record the
  # outcome, the summary and the details file's content, print a JSON line to
  # stdout (which the handler must have routed to fd 3), exit like the real one.
  cat >"$STUB_DIR/pg-rescue" <<'RESCUE'
#!/bin/sh
[ "$1" = result ] || exit 64
{
  echo "outcome=$2"
  echo "summary=$3"
  if [ "$4" = --details-file ]; then sed 's/^/details: /' "$5"; fi
} >>"$RESULT_LOG"
echo "{\"outcome\":\"$2\"}"
case "$2" in resolved) exit 0 ;; declined) exit 2 ;; *) exit 3 ;; esac
RESCUE
  chmod +x "$STUB_DIR/nix" "$STUB_DIR/pg-rescue"

  # Distinct from GFH_REPO (the harness's own seeded repo at $GFH_WORK/repo).
  REPO="$TEST_DIR/handler-repo"
}

teardown() {
  gfh_teardown
}

# The repo under test; `new_repo` creates it with the harness (identity comes
# from the repo's own per-suite config, so no -c overrides are needed).
g() { git -C "$REPO" "$@"; }

new_repo() { gfh_init_repo "$REPO" "pg-rescue-flake-lock-conflict"; }

lock_with_node() { # NAME REV
  printf '{\n  "nodes": {\n    "%s": {\n      "rev": "%s"\n    },\n    "root": {}\n  }\n}\n' "$1" "$2"
}

# A repo on main whose feature branch rebases onto a diverged main, conflicting
# on flake.lock (and on README.md when $1 = readme). Leaves the rebase stopped.
make_conflict() {
  new_repo
  printf '{\n  "nodes": {\n    "root": {}\n  }\n}\n' >"$REPO/flake.lock"
  echo base >"$REPO/README.md"
  g add .
  g commit -q -m seed
  g checkout -q -b feature
  lock_with_node beta b1 >"$REPO/flake.lock"
  [[ ${1:-} != readme ]] || echo feature >"$REPO/README.md"
  g commit -q -am feature
  g checkout -q main
  lock_with_node alpha a1 >"$REPO/flake.lock"
  [[ ${1:-} != readme ]] || echo main >"$REPO/README.md"
  g commit -q -am main
  g checkout -q feature
  g rebase main >/dev/null 2>&1 || true
}

@test "--help prints usage on stdout and exits 0" {
  run "${HANDLER[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-rescue-flake-lock-conflict"* ]]
}

@test "an unexpected argument exits 1, never 2 (which would read as declined)" {
  run "${HANDLER[@]}" nonsense
  [ "$status" -eq 1 ]
}

@test "outside a git work tree it declines" {
  mkdir -p "$TEST_DIR/plain"
  cd "$TEST_DIR/plain"
  run "${HANDLER[@]}"
  [ "$status" -eq 2 ]
  grep -qx 'outcome=declined' "$RESULT_LOG"
}

@test "with no rebase in progress it declines and touches nothing" {
  new_repo
  echo x >"$REPO/flake.lock"
  g add .
  g commit -q -m seed
  cd "$REPO"
  run "${HANDLER[@]}"
  [ "$status" -eq 2 ]
  grep -qx 'summary=No rebase is in progress' "$RESULT_LOG"
  [ ! -e "$NIX_LOG" ]
}

@test "a lock-only conflict is relocked for the conflicted inputs and the rebase completes" {
  make_conflict
  cd "$REPO"
  run "${HANDLER[@]}"
  [ "$status" -eq 0 ]
  [ "$(cat "$NIX_LOG")" = "flake update alpha beta" ]
  grep -qx 'outcome=resolved' "$RESULT_LOG"
  grep -qx 'summary=relocked flake.lock' "$RESULT_LOG"
  [ "$(cat "$REPO/flake.lock")" = "relocked: alpha beta" ]
  [ -z "$(g status --porcelain)" ]
  [ "$(g log --format=%s -n 2 | tr '\n' ,)" = "feature,main," ]
}

@test "stdout carries only the result JSON, nothing from git or nix" {
  make_conflict
  cd "$REPO"
  out="$("${HANDLER[@]}" 2>/dev/null)"
  [ "$out" = '{"outcome":"resolved"}' ]
}

@test "a conflict that also touches another file is declined and left alone" {
  make_conflict readme
  cd "$REPO"
  run "${HANDLER[@]}"
  [ "$status" -eq 2 ]
  grep -qx 'summary=Conflict spans source files, not just flake.lock' "$RESULT_LOG"
  grep -q 'details: README.md' "$RESULT_LOG"
  [ ! -e "$NIX_LOG" ]
  grep -q '<<<<<<<' "$REPO/flake.lock"
}

@test "a failing nix update is a failure (exit 1), not a decline" {
  make_conflict
  printf '#!/bin/sh\nexit 3\n' >"$STUB_DIR/nix"
  cd "$REPO"
  run "${HANDLER[@]}"
  [ "$status" -eq 1 ]
  [ ! -e "$RESULT_LOG" ]
}

@test "it works from a subdirectory of the repository" {
  make_conflict
  mkdir -p "$REPO/sub"
  cd "$REPO/sub"
  run "${HANDLER[@]}"
  [ "$status" -eq 0 ]
  grep -qx 'outcome=resolved' "$RESULT_LOG"
}
