#!/usr/bin/env bats
# bats file_tags=type:unit

setup() {
  # SCRIPTS_DIR/TEST_SUPPORT: injected by nix check (raw src dir / base-flake
  # harness path), or computed relative to this test file for a local
  # `bats tests/` run. MUST honor an already-set env var -- the nix check
  # harness copies tests/* flat into a bare $TMPDIR (so recomputing
  # unconditionally from BATS_TEST_FILENAME would resolve to the wrong
  # directory there), and gfh_setup below scrubs every exported var not on
  # its allowlist -- both are captured into plain locals BEFORE it runs and
  # re-exported after (pg2-31f13).
  local scripts_dir_saved="${SCRIPTS_DIR:-}"
  local test_support_saved="${TEST_SUPPORT:-}"

  if [[ -n $test_support_saved ]]; then
    # shellcheck disable=SC1091
    source "$test_support_saved/git-fixture-harness.bash"
  else
    # shellcheck disable=SC1091
    source "$("$(env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE git -C "$BATS_TEST_DIRNAME" rev-parse --show-toplevel)/tests/support/find-gfh-dir.sh")/git-fixture-harness.bash"
  fi

  if [[ -z $scripts_dir_saved ]]; then
    scripts_dir_saved="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  LIB="${scripts_dir_saved}/wtnew.bash"
  # shellcheck disable=SC1090  # runtime-computed path, by design
  source "$LIB"

  # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
  # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
  # This suite's own `add_worktree` helper creates a REAL linked worktree --
  # exactly the operation pg2-67h4y's write-up shows targeting the CANONICAL
  # clone when GIT_DIR leaks from a commit-hook environment.
  gfh_setup "wtnew"

  export SCRIPTS_DIR="$scripts_dir_saved"

  # Re-export TEST_SUPPORT too (also scrubbed by gfh_setup above) -- a later
  # test resolving the harness path again would otherwise lose it.
  if [[ -n $test_support_saved ]]; then
    export TEST_SUPPORT="$test_support_saved"
  fi

  TEST_DIR="$GFH_REPO"
  # STUB_BIN: dir for fake `integrate-branch-support` placed on PATH.
  # Deliberately OUTSIDE the fixture repo ($TEST_DIR) -- creating it inside
  # would leave the bin dir as untracked content and falsely dirty the repo
  # under test.
  STUB_BIN="$(mktemp -d)"
  cd "$TEST_DIR" || return 1
}

teardown() {
  # gfh_teardown removes GFH_ROOT, which contains TEST_DIR ($GFH_REPO) -- no
  # separate rm -rf "$TEST_DIR" needed.
  gfh_teardown
  [ -n "${STUB_BIN:-}" ] && rm -rf "$STUB_BIN"
  if [ -n "${WT_DIR:-}" ]; then
    rm -rf "$WT_DIR"
  fi
}

add_worktree() {
  WT_DIR="$(mktemp -d)"
  git -C "$TEST_DIR" worktree add -q "$WT_DIR/wt" -b "$1" >/dev/null
  cd "$WT_DIR/wt" || return 1
}

@test "canonical_root: resolves the main worktree from the main worktree itself" {
  run canonical_root
  [ "$status" -eq 0 ]
  # macOS: $TEST_DIR under /tmp resolves to /private/tmp; compare resolved
  # forms both ways.
  [ "$(cd "$output" && pwd -P)" = "$(cd "$TEST_DIR" && pwd -P)" ]
}

@test "canonical_root: resolves the main worktree from inside a linked worktree" {
  add_worktree feat
  run canonical_root
  [ "$status" -eq 0 ]
  [ "$(cd "$output" && pwd -P)" = "$(cd "$TEST_DIR" && pwd -P)" ]
}

@test "wtnew_default_branch: prints the plain name, no drain/ prefix" {
  run wtnew_default_branch "pg2-abcde"
  [ "$status" -eq 0 ]
  [ "$output" = "pg2-abcde" ]
}

@test "wtnew_resolve_base: passes through integrate-branch-support's primary_branch field" {
  cat >"$STUB_BIN/integrate-branch-support" <<'EOF'
#!/usr/bin/env bash
echo '{"strategy":null,"reason":"","primary_branch":"trunk","canonical":{"branch":"main","dirty":false},"remote":null,"open_pr":null,"mr_bead":null}'
EOF
  chmod +x "$STUB_BIN/integrate-branch-support"
  PATH="$STUB_BIN:$PATH"
  run wtnew_resolve_base "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ "$output" = "trunk" ]
}

# --- Task 11 (pg2-pla9d.15) ---------------------------------------------------

# stub_pg_hooks <state> <exit>: a `pg-hooks` on PATH printing that status.
stub_pg_hooks() {
  printf '%s\n' '#!/bin/sh' "printf 'state=%s\\nbundle=\\n' '$1'" "exit $2" >"$STUB_BIN/pg-hooks"
  chmod +x "$STUB_BIN/pg-hooks"
  PATH="$STUB_BIN:$PATH"
}

@test "wtnew_hooks_state: prints the state value, ignoring pg-hooks' non-zero exit" {
  stub_pg_hooks stale 14
  run wtnew_hooks_state "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ "$output" = "stale" ]
}

@test "wtnew_hooks_state: runs pg-hooks inside the given directory" {
  mkdir -p "$TEST_DIR/sub"
  cat >"$STUB_BIN/pg-hooks" <<'EOF'
#!/bin/sh
case "$(pwd -P)" in */sub) echo state=present ;; *) echo state=missing ;; esac
EOF
  chmod +x "$STUB_BIN/pg-hooks"
  PATH="$STUB_BIN:$PATH"
  run wtnew_hooks_state "$TEST_DIR/sub"
  [ "$output" = "present" ]
}

@test "wtnew_hooks_state: prints nothing when pg-hooks is absent (127)" {
  printf '#!/bin/sh\nexit 127\n' >"$STUB_BIN/pg-hooks"
  chmod +x "$STUB_BIN/pg-hooks"
  PATH="$STUB_BIN:$PATH"
  run wtnew_hooks_state "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtnew_hooks_state: prints nothing for an unrecognized state value" {
  stub_pg_hooks weird 0
  run wtnew_hooks_state "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtnew_hooks_state: prints nothing for the retired legacy state" {
  stub_pg_hooks legacy 0
  run wtnew_hooks_state "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtnew_precommit_fact: maps every pg-hooks state onto the shared PRECOMMIT vocabulary" {
  local pair st want
  for pair in present:bundle stale:stale relocated:stale broken:broken missing:missing unreachable:missing; do
    st="${pair%%:*}"
    want="${pair##*:}"
    run wtnew_precommit_fact "$st"
    [ "$status" -eq 0 ]
    [ "$output" = "$want" ]
  done
}

@test "wtnew_precommit_fact: no state, an unrecognized state, and the retired legacy state all report missing" {
  run wtnew_precommit_fact ""
  [ "$output" = "missing" ]
  run wtnew_precommit_fact weird
  [ "$output" = "missing" ]
  run wtnew_precommit_fact legacy
  [ "$output" = "missing" ]
}

@test "wtnew_precommit_fact: agrees with integrate-branch-support --facts for every state (drift guard)" {
  # Only meaningful against an integrate-branch-support that has the pg-hooks
  # PRECOMMIT vocabulary (the --bundle-refresh era); an older build on PATH
  # derives PRECOMMIT from the on-disk config alone.
  command -v integrate-branch-support >/dev/null 2>&1 || skip "integrate-branch-support not on PATH"
  integrate-branch-support --help | grep -q -e '--bundle-refresh' || skip "integrate-branch-support predates the pg-hooks PRECOMMIT vocabulary"
  local st stub_status
  for st in present stale relocated broken legacy missing unreachable; do
    printf '%s\n' '#!/bin/sh' "printf 'state=%s\\nbundle=\\n' '$st'" "exit 0" >"$STUB_BIN/pg-hooks"
    chmod +x "$STUB_BIN/pg-hooks"
    stub_status="$(PATH="$STUB_BIN:$PATH" integrate-branch-support --facts | sed -n 's/^PRECOMMIT=//p')"
    # A build that still maps the retired legacy state to "legacy" predates
    # the legacy removal; the nix check builds the in-tree tool, which agrees.
    if [ "$st" = legacy ] && [ "$stub_status" = legacy ]; then continue; fi
    [ "$stub_status" = "$(wtnew_precommit_fact "$st")" ]
  done
}
