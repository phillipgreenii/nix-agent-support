#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests for pg-rescue-flake-lock-conflict (bead pg2-3ybxg): real
# git against scratch repositories, a fake `nix` that writes a deterministic
# lock (no network), and a stub `pg-rescue` that records its `result` call.
# The wrapper-driven tests (the REAL pg-rescue running this handler) live in
# packages/pg-rescue/cmd/pg-rescue/flakelock_handler_test.go.

setup() {
  # Hook environment: a `git commit` from a linked worktree exports GIT_DIR and
  # friends into the pre-commit hook, and this suite inherits them from the
  # run-unit-tests hook. Drop every GIT_* variable so a fixture can never
  # resolve to (and mutate) the real repository.
  local v
  for v in $(compgen -e | grep '^GIT_' || true); do unset "$v"; done

  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    HANDLER=("$SCRIPT_UNDER_TEST")
  else
    HANDLER=(bash -euo pipefail "$SCRIPTS_DIR/pg-rescue-flake-lock-conflict.sh")
  fi

  TEST_DIR="$(mktemp -d)"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
  export GIT_CEILING_DIRECTORIES="$TEST_DIR"
  # The handler's `git rebase --continue` makes commits, so it needs an identity.
  export GIT_AUTHOR_NAME=T GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=T GIT_COMMITTER_EMAIL=t@example.com

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

  REPO="$TEST_DIR/repo"
}

teardown() {
  rm -rf "$TEST_DIR"
}

g() { git -C "$REPO" -c user.name=T -c user.email=t@example.com -c commit.gpgsign=false "$@"; }

lock_with_node() { # NAME REV
  printf '{\n  "nodes": {\n    "%s": {\n      "rev": "%s"\n    },\n    "root": {}\n  }\n}\n' "$1" "$2"
}

# A repo on main whose feature branch rebases onto a diverged main, conflicting
# on flake.lock (and on README.md when $1 = readme). Leaves the rebase stopped.
make_conflict() {
  mkdir -p "$REPO"
  g init -q -b main
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
  mkdir -p "$REPO"
  g init -q -b main
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
