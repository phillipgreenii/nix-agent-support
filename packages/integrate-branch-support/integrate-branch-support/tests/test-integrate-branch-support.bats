#!/usr/bin/env bats
# bats file_tags=type:unit

setup() {
  # SCRIPTS_DIR: injected by nix check (raw src dir), or computed relative to
  # this test file for a local `bats tests/` run. MUST honor an already-set
  # env var — the nix check harness copies tests/* flat into a bare $TMPDIR,
  # so recomputing unconditionally from BATS_TEST_FILENAME would resolve to
  # the wrong directory there. Captured into a plain local BEFORE gfh_setup
  # runs, since it scrubs every exported var not on its allowlist — SCRIPTS_DIR
  # is exported by the nix check, so it would otherwise be wiped (pg2-31f13).
  local scripts_dir_saved="${SCRIPTS_DIR:-}"
  local test_support_saved="${TEST_SUPPORT:-}"

  if [[ -n $test_support_saved ]]; then
    # shellcheck disable=SC1091
    source "$test_support_saved/git-fixture-harness.bash"
  else
    # shellcheck disable=SC1091
    source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/git-fixture-harness.bash"
  fi

  # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
  # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
  # This suite's own `add_worktree` helper creates a REAL linked worktree —
  # exactly the operation pg2-67h4y's write-up shows targeting the CANONICAL
  # clone when GIT_DIR leaks from a commit-hook environment.
  gfh_setup "integrate-branch-support"

  if [[ -z $scripts_dir_saved ]]; then
    scripts_dir_saved="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  export SCRIPTS_DIR="$scripts_dir_saved"

  # Re-export TEST_SUPPORT too (also scrubbed by gfh_setup above) -- the
  # regression-guard test below needs it to resolve the harness path again.
  if [[ -n $test_support_saved ]]; then
    export TEST_SUPPORT="$test_support_saved"
  fi

  BIN="${SCRIPTS_DIR}/integrate-branch-support.sh"
  TEST_DIR="$GFH_REPO"
  # STUB_BIN: dir for fake gh/bd executables placed on PATH. Deliberately
  # OUTSIDE the fixture repo ($TEST_DIR) — creating it inside would leave the
  # bin dir as untracked content and falsely dirty the repo under test.
  STUB_BIN="$(mktemp -d)"
  cd "$TEST_DIR" || return 1
}

teardown() {
  # gfh_teardown removes GFH_ROOT, which contains TEST_DIR ($GFH_REPO) — no
  # separate rm -rf "$TEST_DIR" needed.
  gfh_teardown
  [ -n "${STUB_BIN:-}" ] && rm -rf "$STUB_BIN"
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

# link_real_tools: symlink into $STUB_BIN the real tools the tool and this
# harness need at runtime (bash/git/jq/coreutils), so an "optional source
# absent" test can set PATH="$STUB_BIN" alone and thereby run with gh/bd
# genuinely ABSENT (they are never linked here) while real tools still resolve.
# This replaces a literal `PATH="$STUB_BIN:/usr/bin:/bin"`, which carries NO
# bash/coreutils in the nix build sandbox (and none on NixOS, where /usr/bin
# holds only `env` and /bin only `sh`) -- there `run bash "$BIN"` and
# teardown's `rm` exited 127 and the tests failed. gh/bd absence is the POINT
# of those tests: they exercise the tool's `command -v gh`/`command -v bd`
# graceful-degradation branch. Resolving each tool's real path (rather than
# adding a fixed dir to PATH) is layout-independent: on NixOS gh/bd live in the
# same profile bin dir as git/jq/bash, so dropping a dir cannot scrub them --
# only linking a curated allow-list can. MUST be called while PATH is still the
# default (so `command -v`/`ln` resolve), i.e. BEFORE setting PATH="$STUB_BIN".
link_real_tools() {
  local t p
  for t in bash git jq dirname rm mktemp cat; do
    p="$(command -v "$t" 2>/dev/null)" && ln -s "$p" "$STUB_BIN/$t"
  done
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

# Regression (bead tc-md1e0): from inside a LINKED WORKTREE whose feature
# branch has no upstream, remote resolution MUST anchor to the canonical
# clone (whose branch tracks a real remote), not the worktree's current
# branch. Before the fix, the worktree's branch had no '@{upstream}', so the
# fallback counted the repo's TWO remotes and reported "ambiguous" ->
# remote:null, even though the canonical branch tracks origin/main. This is
# exactly the homelab case (origin+bitbucket, main tracks origin/main), which
# also runs from a flake SUBDIRECTORY (nix/) inside the workforest worktree.
@test "remote: resolves the canonical branch's upstream from inside a linked worktree" {
  git remote add origin https://example.invalid/o/r.git
  git remote add bitbucket https://example.invalid/o2/r2.git
  git update-ref refs/remotes/origin/main HEAD
  git branch --set-upstream-to=origin/main main
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == "origin"'
}

@test "remote: resolves from a flake SUBDIRECTORY inside a linked worktree (homelab repro)" {
  git remote add origin https://example.invalid/o/r.git
  git remote add bitbucket https://example.invalid/o2/r2.git
  git update-ref refs/remotes/origin/main HEAD
  git branch --set-upstream-to=origin/main main
  mkdir -p nix
  echo "{}" >nix/flake.nix
  git add -A
  git -c user.email=t@t -c user.name=t commit -q -m "add nix/ flake subdir"
  add_worktree feat
  cd nix || return 1
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == "origin"'
}

@test "remote: sole remote still resolves from inside a linked worktree with no upstream" {
  git remote add origin https://example.invalid/o/r.git
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == "origin"'
}

@test "remote: genuinely ambiguous (two remotes, no upstream anywhere) stays ambiguous from a worktree" {
  git remote add origin https://example.invalid/o/r.git
  git remote add bitbucket https://example.invalid/o2/r2.git
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == null and (.reason | test("ambig"; "i"))'
}

@test "remote: zero remotes stays null from inside a linked worktree" {
  add_worktree feat
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.remote == null'
}

@test "open_pr: an open PR surfaces as open_pr.number" {
  mkdir -p "$STUB_BIN"
  cat >"$STUB_BIN/gh" <<'EOF'
#!/usr/bin/env bash
echo '{"number":42,"state":"OPEN","url":"https://example.invalid/o/r/pull/42"}'
EOF
  chmod +x "$STUB_BIN/gh"
  PATH="$STUB_BIN:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.open_pr.number == 42'
}

@test "open_pr: a merged PR is treated as no open PR" {
  mkdir -p "$STUB_BIN"
  cat >"$STUB_BIN/gh" <<'EOF'
#!/usr/bin/env bash
echo '{"number":42,"state":"MERGED","url":"https://example.invalid/o/r/pull/42"}'
EOF
  chmod +x "$STUB_BIN/gh"
  PATH="$STUB_BIN:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.open_pr == null'
}

@test "mr_bead: a merge-request bead surfaces as mr_bead" {
  mkdir -p "$STUB_BIN"
  cat >"$STUB_BIN/bd" <<'EOF'
#!/usr/bin/env bash
echo '{"data":[{"id":"pg2-abcd"}],"schema_version":1}'
EOF
  chmod +x "$STUB_BIN/bd"
  PATH="$STUB_BIN:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.mr_bead == "pg2-abcd"'
}

@test "gh/bd absent: open_pr and mr_bead are null and the tool still exits 0" {
  mkdir -p "$STUB_BIN"
  link_real_tools
  PATH="$STUB_BIN"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.open_pr == null and .mr_bead == null'
}

@test "strategy: declared ff-merge-to-main wins outright" {
  git config pgii-integrate-branch.strategy ff-merge-to-main
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.strategy == "ff-merge-to-main" and (.reason | test("declared"))'
}

@test "strategy: no remote and undeclared infers ff-merge-to-main" {
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.strategy == "ff-merge-to-main" and (.reason | test("no remote"; "i"))'
}

@test "strategy: an open PR (undeclared, remote present) infers pull-request" {
  git remote add origin https://example.invalid/o/r.git
  mkdir -p "$STUB_BIN"
  cat >"$STUB_BIN/gh" <<'EOF'
#!/usr/bin/env bash
echo '{"number":42,"state":"OPEN","url":"https://example.invalid/o/r/pull/42"}'
EOF
  chmod +x "$STUB_BIN/gh"
  PATH="$STUB_BIN:$PATH"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.strategy == "pull-request"'
}

@test "strategy: an open merge-request bead (undeclared, remote present) infers pull-request" {
  git remote add origin https://example.invalid/o/r.git
  mkdir -p "$STUB_BIN"
  cat >"$STUB_BIN/bd" <<'EOF'
#!/usr/bin/env bash
echo '{"data":[{"id":"pg2-abcd"}],"schema_version":1}'
EOF
  chmod +x "$STUB_BIN/bd"
  link_real_tools
  PATH="$STUB_BIN"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.strategy == "pull-request"'
}

@test "strategy: remote present, no PR/bead, undeclared cannot be inferred" {
  git remote add origin https://example.invalid/o/r.git
  mkdir -p "$STUB_BIN"
  link_real_tools
  PATH="$STUB_BIN"
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.strategy == null'
}

@test "strategy: declared pull-request with no remote is flagged infeasible, not overridden" {
  git config pgii-integrate-branch.strategy pull-request
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.strategy == "pull-request" and (.reason | test("infeasible"; "i"))'
}

@test "fail-safe: exits nonzero when run outside a git repository" {
  local nogit_dir
  nogit_dir="$(mktemp -d)"
  cd "$nogit_dir" || return 1
  run bash "$BIN"
  cd "$TEST_DIR" || true
  rm -rf "$nogit_dir"
  [ "$status" -ne 0 ]
}

@test "regression: a GIT_DIR/GIT_INDEX_FILE leaked into the parent shell before setup is scrubbed, not honored" {
  # Simulates the pg2-67h4y hook-environment leak: GIT_DIR/GIT_INDEX_FILE
  # pointed at a bogus path BEFORE the harness's own setup runs. If the
  # scrub (gfh_reset_env, called by gfh_setup) did not take effect, the
  # add_worktree helper's `git worktree add` -- the exact operation pg2-67h4y
  # documents targeting a real canonical clone -- would operate against the
  # bogus path instead of this test's own fixture repo.
  local bogus_parent bogus harness_path
  bogus_parent="$(mktemp -d)"
  bogus="$bogus_parent/leaked-gitdir"
  if [[ -n ${TEST_SUPPORT:-} ]]; then
    harness_path="$TEST_SUPPORT/git-fixture-harness.bash"
  else
    harness_path="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/git-fixture-harness.bash"
  fi

  run env GIT_DIR="$bogus" GIT_INDEX_FILE="$bogus/index" HARNESS_PATH="$harness_path" bash -c '
    source "$HARNESS_PATH"
    gfh_setup "integrate-branch-support-regression"
    command git -C "$GFH_REPO" rev-parse --git-dir
  '
  [ "$status" -eq 0 ]
  [[ "$output" != *"leaked-gitdir"* ]]

  # The bogus path must never have been created -- proves the scrub took
  # effect rather than the leaked vars silently being honored.
  [ ! -e "$bogus" ]

  rm -rf "$bogus_parent"
}
