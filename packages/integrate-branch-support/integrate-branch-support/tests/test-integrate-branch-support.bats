#!/usr/bin/env bats

setup() {
  # SCRIPTS_DIR: injected by nix check (raw src dir), or computed relative to
  # this test file for a local `bats tests/` run. MUST honor an already-set
  # env var — the nix check harness copies tests/* flat into a bare $TMPDIR,
  # so recomputing unconditionally from BATS_TEST_FILENAME would resolve to
  # the wrong directory there.
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  BIN="${SCRIPTS_DIR}/integrate-branch-support.sh"
  TEST_DIR="$(mktemp -d)"
  cd "$TEST_DIR" || return 1
  git init -q --initial-branch=main
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
}

teardown() {
  rm -rf "$TEST_DIR"
  # WT_DIR: set only by tests that create a linked worktree (see add_worktree
  # below); cleaned up here so it doesn't leak into the shared temp area.
  if [ -n "${WT_DIR:-}" ]; then
    rm -rf "$WT_DIR"
  fi
}

# add_worktree <branch>: create a linked worktree on <branch>, checked out
# from the main tree ($TEST_DIR) into a *separate* temp dir, then cd into it.
# Deliberately NOT nested under $TEST_DIR: git has no special-case for a
# linked worktree living inside its own main tree's working directory, so
# nesting would leave the freshly created worktree dir itself as untracked
# content — falsely dirtying the canonical clone before the test even runs.
add_worktree() {
  WT_DIR="$(mktemp -d)"
  git -C "$TEST_DIR" worktree add -q "$WT_DIR/wt" -b "$1" >/dev/null
  cd "$WT_DIR/wt" || return 1
}

@test "prints valid JSON with a strategy field" {
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e 'has("strategy")'
}

@test "primary branch: honors pgii-integrate-branch.primaryBranch" {
  git config pgii-integrate-branch.primaryBranch trunk
  run bash "$BIN"; echo "$output" | jq -e '.primary_branch == "trunk"'
}

@test "primary branch: defaults to main when unset and no origin" {
  run bash "$BIN"; echo "$output" | jq -e '.primary_branch == "main"'
}

@test "primary branch: falls back to origin/HEAD when config is unset" {
  git symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/develop
  run bash "$BIN"; echo "$output" | jq -e '.primary_branch == "develop"'
}

@test "canonical: reports the main worktree's branch/dirty from inside a worktree" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.canonical.branch=="main" and .canonical.dirty==false'
}

@test "canonical: dirty becomes true when the main worktree (not the linked one) has uncommitted changes" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  echo x >"$TEST_DIR/f"
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.canonical.dirty==true'
}

@test "canonical: branch reflects the main worktree's actual checked-out branch, not the linked one's" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  git checkout -q -b develop
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.canonical.branch=="develop"'
}

@test "remote: reports null when the repo has no remote" {
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == null'
}

@test "remote: uses the sole remote when no upstream is configured" {
  git remote add origin https://example.invalid/o/r.git
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == "origin"'
}

@test "remote: prefers the branch's upstream remote even when another remote also exists" {
  git remote add origin https://example.invalid/o/r.git
  git remote add fork https://example.invalid/o2/r2.git
  git update-ref refs/remotes/fork/main HEAD
  git branch --set-upstream-to=fork/main main
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == "fork"'
}

@test "remote: two remotes with no upstream set is ambiguous" {
  git remote add origin https://example.invalid/o/r.git
  git remote add fork https://example.invalid/o2/r2.git
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == null and (.reason | test("ambig"; "i"))'
}

@test "open_pr: an open PR surfaces as open_pr.number" {
  mkdir -p "$TEST_DIR/bin"
  cat >"$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo '{"number":42,"state":"OPEN","url":"https://example.invalid/o/r/pull/42"}'
EOF
  chmod +x "$TEST_DIR/bin/gh"
  PATH="$TEST_DIR/bin:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.open_pr.number == 42'
}

@test "open_pr: a merged PR is treated as no open PR" {
  mkdir -p "$TEST_DIR/bin"
  cat >"$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo '{"number":42,"state":"MERGED","url":"https://example.invalid/o/r/pull/42"}'
EOF
  chmod +x "$TEST_DIR/bin/gh"
  PATH="$TEST_DIR/bin:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.open_pr == null'
}

@test "mr_bead: a merge-request bead surfaces as mr_bead" {
  mkdir -p "$TEST_DIR/bin"
  cat >"$TEST_DIR/bin/bd" <<'EOF'
#!/usr/bin/env bash
echo '{"data":[{"id":"pg2-abcd"}],"schema_version":1}'
EOF
  chmod +x "$TEST_DIR/bin/bd"
  PATH="$TEST_DIR/bin:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.mr_bead == "pg2-abcd"'
}

@test "gh/bd absent: open_pr and mr_bead are null and the tool still exits 0" {
  mkdir -p "$TEST_DIR/bin"
  PATH="$TEST_DIR/bin:/usr/bin:/bin"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.open_pr == null and .mr_bead == null'
}
