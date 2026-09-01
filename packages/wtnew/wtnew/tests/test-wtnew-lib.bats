#!/usr/bin/env bats
# bats file_tags=type:unit

setup() {
  # SCRIPTS_DIR: injected by nix check (raw src dir), or computed relative to
  # this test file for a local `bats tests/` run. MUST honor an already-set
  # env var -- the nix check harness copies tests/* flat into a bare $TMPDIR,
  # so recomputing unconditionally from BATS_TEST_FILENAME would resolve to
  # the wrong directory there.
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  LIB="${SCRIPTS_DIR}/wtnew.bash"
  # shellcheck disable=SC1090  # runtime-computed path, by design
  source "$LIB"
  TEST_DIR="$(mktemp -d)"
  # STUB_BIN: dir for fake `integrate-branch-support` placed on PATH.
  # Deliberately OUTSIDE the fixture repo ($TEST_DIR) -- creating it inside
  # would leave the bin dir as untracked content and falsely dirty the repo
  # under test.
  STUB_BIN="$(mktemp -d)"
  cd "$TEST_DIR" || return 1
  git init -q --initial-branch=main
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
}

teardown() {
  rm -rf "$TEST_DIR"
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

@test "wtnew_link_precommit_config: SRC is a symlink -> DST is relinked to SRC's resolved target (not SRC itself)" {
  mkdir -p "$TEST_DIR/store-target"
  touch "$TEST_DIR/store-target/config.yaml"
  ln -s "$TEST_DIR/store-target/config.yaml" "$TEST_DIR/.pre-commit-config.yaml"
  mkdir -p "$TEST_DIR/wt"
  run wtnew_link_precommit_config "$TEST_DIR/.pre-commit-config.yaml" "$TEST_DIR/wt/.pre-commit-config.yaml"
  [ "$status" -eq 0 ]
  [ "$output" = "linked" ]
  [ -L "$TEST_DIR/wt/.pre-commit-config.yaml" ]
  [ "$(readlink "$TEST_DIR/wt/.pre-commit-config.yaml")" = "$TEST_DIR/store-target/config.yaml" ]
}

@test "wtnew_link_precommit_config: SRC is a plain file -> DST gets a literal copy, not a symlink" {
  echo "repos: []" >"$TEST_DIR/.pre-commit-config.yaml"
  mkdir -p "$TEST_DIR/wt"
  run wtnew_link_precommit_config "$TEST_DIR/.pre-commit-config.yaml" "$TEST_DIR/wt/.pre-commit-config.yaml"
  [ "$status" -eq 0 ]
  [ "$output" = "copied" ]
  [ ! -L "$TEST_DIR/wt/.pre-commit-config.yaml" ]
  [ -f "$TEST_DIR/wt/.pre-commit-config.yaml" ]
  diff "$TEST_DIR/.pre-commit-config.yaml" "$TEST_DIR/wt/.pre-commit-config.yaml"
}

@test "wtnew_link_precommit_config: SRC is absent -> nothing created, reported as none" {
  mkdir -p "$TEST_DIR/wt"
  run wtnew_link_precommit_config "$TEST_DIR/.pre-commit-config.yaml" "$TEST_DIR/wt/.pre-commit-config.yaml"
  [ "$status" -eq 0 ]
  [ "$output" = "none" ]
  [ ! -e "$TEST_DIR/wt/.pre-commit-config.yaml" ]
}
