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

@test "facts: unrecognized argument is a usage error" {
  run bash "$BIN" --bogus
  [ "$status" -ne 0 ]
}

@test "facts: --facts prints the documented KEY=value block" {
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  for key in WT FB CC PRIMARY DIRTY AHEAD BEHIND PRECOMMIT CC_CORE_WORKTREE; do
    echo "$output" | grep -qE "^${key}=" || {
      echo "missing key: $key" >&2
      return 1
    }
  done
}

@test "facts: WT/CC both resolve to the real (symlink-free) repo path from the main worktree" {
  local real_test_dir
  real_test_dir="$(cd "$TEST_DIR" && pwd -P)"
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -qF "WT=$real_test_dir"
  echo "$output" | grep -qF "CC=$real_test_dir"
}

@test "facts: WT is the linked worktree and CC is the main worktree, from inside a linked worktree" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  local real_test_dir
  real_test_dir="$(cd "$TEST_DIR" && pwd -P)"
  add_worktree feat
  local real_wt_dir
  real_wt_dir="$(pwd -P)"
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -qF "WT=$real_wt_dir"
  echo "$output" | grep -qF "CC=$real_test_dir"
}

@test "facts: FB reports the current branch, distinct from the canonical clone's branch" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^FB=feat$"
}

@test "facts: FB reports (detached) on a detached HEAD" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  git checkout -q --detach HEAD
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^FB=(detached)$"
}

@test "facts: DIRTY reflects the CURRENT worktree, not the canonical clone" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^DIRTY=no$"
  echo x >f
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^DIRTY=yes$"
}

@test "facts: AHEAD/BEHIND count commits relative to the primary branch" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^AHEAD=0$"
  echo "$output" | grep -q "^BEHIND=0$"

  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m "feat commit"
  git -C "$TEST_DIR" -c user.email=t@t -c user.name=t commit -q --allow-empty -m "main commit"
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^AHEAD=1$"
  echo "$output" | grep -q "^BEHIND=1$"
}

@test "facts: AHEAD/BEHIND degrade to 0/0 when the primary branch cannot be resolved" {
  git config pgii-integrate-branch.primaryBranch does-not-exist
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^PRIMARY=does-not-exist$"
  echo "$output" | grep -q "^AHEAD=0$"
  echo "$output" | grep -q "^BEHIND=0$"
}

# stub_pg_hooks [exit-code] [stderr-text]: put a fake `pg-hooks` on PATH. Every
# call records its cwd and argv to $STUB_BIN/pg-hooks-calls (outside the
# fixture repo, so recording never dirties the tree under test); the ABSENCE
# of that file proves pg-hooks was NOT called. `pg-hooks status` prints
# state=$STUB_PGH_STATE (when set) and exits $STUB_PGH_STATUS_RC (default 0);
# any other subcommand writes [stderr-text] to stderr and exits [exit-code].
stub_pg_hooks() {
  local rc="${1:-0}" msg="${2:-}"
  printf '%s' "$msg" >"$STUB_BIN/pg-hooks-stderr"
  cat >"$STUB_BIN/pg-hooks" <<STUB
#!/usr/bin/env bash
printf 'cwd=%s\n' "\$(pwd -P)" >>"$STUB_BIN/pg-hooks-calls"
printf 'args=%s\n' "\$*" >>"$STUB_BIN/pg-hooks-calls"
if [ "\$1" = status ]; then
  if [ -n "\${STUB_PGH_STATE:-}" ]; then
    printf 'state=%s\nbundle=\ngeneration=\nstages=\nreinstall=\n' "\$STUB_PGH_STATE"
  fi
  exit "\${STUB_PGH_STATUS_RC:-0}"
fi
if [ -s "$STUB_BIN/pg-hooks-stderr" ]; then
  cat "$STUB_BIN/pg-hooks-stderr" >&2
  echo >&2
fi
exit $rc
STUB
  chmod +x "$STUB_BIN/pg-hooks"
  PATH="$STUB_BIN:$PATH"
}

# run_pg_hooks_absent <args...>: run the tool with PATH reduced to the linked
# real tools only, so pg-hooks and bgrun are genuinely absent, then restore PATH
# (the suite's teardown needs the real rm).
run_pg_hooks_absent() {
  local saved_path="$PATH"
  link_real_tools
  ln -s "$(command -v basename)" "$STUB_BIN/basename"
  PATH="$STUB_BIN"
  run bash "$BIN" "$@"
  PATH="$saved_path"
}

# facts_precommit <pg-hooks porcelain state> <expected PRECOMMIT> [status rc]
facts_precommit() {
  STUB_PGH_STATE="$1"
  STUB_PGH_STATUS_RC="${3:-0}"
  export STUB_PGH_STATE STUB_PGH_STATUS_RC
  stub_pg_hooks
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -qx "PRECOMMIT=$2"
  grep -qxF "args=status --porcelain" "$STUB_BIN/pg-hooks-calls"
}

@test "facts: PRECOMMIT is bundle when pg-hooks status reports present" {
  facts_precommit present bundle
}

@test "facts: PRECOMMIT is stale when pg-hooks status reports stale (non-zero exit ignored)" {
  facts_precommit stale stale 14
}

@test "facts: PRECOMMIT is stale when pg-hooks status reports relocated" {
  facts_precommit relocated stale 16
}

@test "facts: PRECOMMIT is missing when pg-hooks status reports the retired legacy state" {
  facts_precommit legacy missing
}

@test "facts: PRECOMMIT is missing when pg-hooks status reports missing" {
  facts_precommit missing missing 13
}

@test "facts: PRECOMMIT is broken when pg-hooks status reports broken" {
  facts_precommit broken broken 12
}

@test "facts: PRECOMMIT is missing when pg-hooks status reports unreachable" {
  facts_precommit unreachable missing 15
}

@test "facts: pg-hooks status runs from the worktree root of a linked worktree" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  local wt
  wt="$(pwd -P)"
  mkdir -p sub
  cd sub || return 1
  STUB_PGH_STATE=present
  export STUB_PGH_STATE
  stub_pg_hooks
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  grep -qxF "cwd=$wt" "$STUB_BIN/pg-hooks-calls"
}

@test "facts: PRECOMMIT falls back to missing when pg-hooks is absent" {
  run_pg_hooks_absent --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^PRECOMMIT=missing$"
}

@test "facts: PRECOMMIT is missing when pg-hooks prints no recognizable state, even with an on-disk config" {
  echo "repos: []" >.pre-commit-config.yaml
  STUB_PGH_STATE=""
  export STUB_PGH_STATE
  stub_pg_hooks
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^PRECOMMIT=missing$"
}

@test "facts: PRIMARY resolution matches the JSON mode's (config -> origin/HEAD -> main)" {
  git symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/develop
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "^PRIMARY=develop$"
}

@test "facts: CC_CORE_WORKTREE is empty on a healthy canonical clone" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -qx "CC_CORE_WORKTREE="
}

# pg2-lcxpf / pg2-4c4nv: a stray core.worktree in the canonical .git/config
# makes git report ANOTHER path as the canonical toplevel (CC lies) and shows a
# phantom dirty tree. --facts must name the cause, read-only.
@test "facts: CC_CORE_WORKTREE reports a core.worktree set in the canonical config, from a linked worktree, without clearing it" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  local real_test_dir decoy
  real_test_dir="$(cd "$TEST_DIR" && pwd -P)"
  decoy="$(mktemp -d)"
  decoy="$(cd "$decoy" && pwd -P)"
  add_worktree feat
  git -C "$TEST_DIR" config core.worktree "$decoy"
  run bash "$BIN" --facts
  [ "$status" -eq 0 ]
  echo "$output" | grep -qxF "CC_CORE_WORKTREE=$decoy"
  # Read-only (R-3): the key is still set in the canonical config afterwards.
  [ "$(git config --file "$real_test_dir/.git/config" --get core.worktree)" = "$decoy" ]
  rm -rf "$decoy"
}

# PG_HOOKS_NOTICE: the exact line spec 5.3 prescribes when pg-hooks is absent.
PG_HOOKS_NOTICE='pg-hooks not installed on this machine; ask the operator to run pn workspace apply'

# assert_no_hook_config_created <worktree>: FF-1b MUST NOT link, copy, or
# regenerate a hook config -- nothing at all may exist at the old config
# path, and the worktree MUST be exactly as clean as before.
assert_no_hook_config_created() {
  [ ! -e "$1/.pre-commit-config.yaml" ]
  [ ! -L "$1/.pre-commit-config.yaml" ]
  [ -z "$(git -C "$1" status --porcelain)" ]
}

@test "prek-branch-diff: runs pg-hooks run pre-land <FB> from the WT root" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  local wt
  wt="$(pwd -P)"
  mkdir -p sub
  cd sub || return 1
  stub_pg_hooks 0
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 0 ]
  [ -f "$STUB_BIN/pg-hooks-calls" ]
  grep -qxF "cwd=$wt" "$STUB_BIN/pg-hooks-calls"
  grep -qxF "args=run pre-land feat" "$STUB_BIN/pg-hooks-calls"
  [ "$(grep -c '^args=' "$STUB_BIN/pg-hooks-calls")" -eq 1 ]
}

@test "prek-branch-diff: exit 10 (a hook failed) is passed through as 10" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  stub_pg_hooks 10 "pg-hooks: pre-land hooks failed in repo"
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 10 ]
  [[ "$output" == *"pg-hooks: pre-land hooks failed in repo"* ]]
}

@test "prek-branch-diff: exit 13 (no bundle) relays pg-hooks's notice verbatim and exits 0" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  local wt notice
  wt="$(pwd -P)"
  notice="pg-hooks: no hook bundle for repo (shared bundle lives in /cc); pre-land hooks not run. Fix: (cd /cc && nix run .#install-pre-commit-hooks)"
  stub_pg_hooks 13 "$notice"
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ "$output" = "$notice" ]
  assert_no_hook_config_created "$wt"
}

@test "prek-branch-diff: exit 127 from pg-hooks records the not-installed line and exits 0" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  stub_pg_hooks 127
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ "$output" = "$PG_HOOKS_NOTICE" ]
}

@test "prek-branch-diff: pg-hooks absent from PATH records the not-installed line, exits 0, creates nothing" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  local wt
  wt="$(pwd -P)"
  run_pg_hooks_absent --prek-branch-diff
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ "$output" = "$PG_HOOKS_NOTICE" ]
  assert_no_hook_config_created "$wt"
}

@test "prek-branch-diff: other failures (12 broken bundle, 2 usage) pass through unchanged" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  stub_pg_hooks 12 "pg-hooks: hook bundle for repo is broken (bin/prek is not executable). Rebuild: x"
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 12 ]
  [[ "$output" == *"is broken"* ]]
  stub_pg_hooks 2 "pg-hooks: run pre-land: cannot resolve the primary branch: main"
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 2 ]
}

@test "prek-branch-diff: never calls prek directly" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  add_worktree feat
  cat >"$STUB_BIN/prek" <<STUB
#!/usr/bin/env bash
echo called >>"$STUB_BIN/prek-calls"
STUB
  chmod +x "$STUB_BIN/prek"
  stub_pg_hooks 0
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 0 ]
  [ ! -e "$STUB_BIN/prek-calls" ]
  grep -qxF "args=run pre-land feat" "$STUB_BIN/pg-hooks-calls"
}

@test "prek-branch-diff: detached HEAD is a usage-style failure and pg-hooks is not called" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  git checkout -q --detach
  stub_pg_hooks 0
  run bash "$BIN" --prek-branch-diff
  [ "$status" -eq 1 ]
  [[ "$output" == *"detached HEAD"* ]]
  [ ! -e "$STUB_BIN/pg-hooks-calls" ]
}

# --- --bundle-refresh (FF-4) -------------------------------------------------

# stub_bgrun [exit-code] [stdout]: fake `bgrun` recording argv (one arg per
# line, after a "call" marker) to $STUB_BIN/bgrun-calls.
stub_bgrun() {
  local rc="${1:-0}" out="${2:-pg-hooks-refresh 4242 /tmp/bg.log}"
  cat >"$STUB_BIN/bgrun" <<STUB
#!/usr/bin/env bash
echo call >>"$STUB_BIN/bgrun-calls"
for a in "\$@"; do printf 'arg=%s\n' "\$a" >>"$STUB_BIN/bgrun-calls"; done
printf '%s\n' '$out'
exit $rc
STUB
  chmod +x "$STUB_BIN/bgrun"
  PATH="$STUB_BIN:$PATH"
}

# make_bundle_state [stampPath]: give the canonical clone ($TEST_DIR) the
# pg-hooks layout `pg-hooks-install` writes: a regular-file `current`, a
# one-line `reinstall` command (a marker-touching one, so a test can prove the
# tool never runs it itself), and a bundle dir whose meta.json lists stampPaths.
make_bundle_state() {
  local common
  common="$(git rev-parse --path-format=absolute --git-common-dir)"
  mkdir -p "$common/pg-hooks/gen-1/bundle"
  printf 'gen-1\n' >"$common/pg-hooks/current"
  printf 'touch %s/reinstall-ran\n' "$STUB_BIN" >"$common/pg-hooks/reinstall"
  jq -n --arg p "${1:-}" '{repo: "r", stampPaths: (if $p == "" then [] else [$p] end), stages: ["pre-commit"]}' \
    >"$common/pg-hooks/gen-1/bundle/meta.json"
}

# land_commit <path>: commit a change to <path> and print the pre-change sha
# (what the handler captures before FF-2b).
land_commit() {
  local old
  old="$(git rev-parse HEAD)"
  mkdir -p "$(dirname "$1")"
  echo "$RANDOM" >>"$1"
  git add "$1"
  git -c user.email=t@t -c user.name=t commit -q -m "touch $1"
  printf '%s' "$old"
}

@test "bundle-refresh: a landed flake.lock change starts the reinstall command via bgrun" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state
  stub_bgrun
  local old
  old="$(land_commit flake.lock)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [[ "$output" == "FF-4: bundle refresh started in the background"* ]]
  [[ "$output" == *"bgcheck pg-hooks-refresh-"* ]]
  [ "$(grep -c '^call$' "$STUB_BIN/bgrun-calls")" -eq 1 ]
  sed -n '2p' "$STUB_BIN/bgrun-calls" | grep -q '^arg=pg-hooks-refresh-'
  grep -qxF 'arg=--' "$STUB_BIN/bgrun-calls"
  grep -qxF 'arg=bash' "$STUB_BIN/bgrun-calls"
  grep -qxF 'arg=-c' "$STUB_BIN/bgrun-calls"
  grep -qxF "arg=touch $STUB_BIN/reinstall-ran" "$STUB_BIN/bgrun-calls"
  # The tool never runs the reinstall command itself.
  [ ! -e "$STUB_BIN/reinstall-ran" ]
}

@test "bundle-refresh: a flake.nix change starts the refresh" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state
  stub_bgrun
  local old
  old="$(land_commit flake.nix)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [[ "$output" == "FF-4: bundle refresh started"* ]]
}

@test "bundle-refresh: a change under a meta.json stampPaths entry starts the refresh" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state modules/hooks
  stub_bgrun
  local old
  old="$(land_commit modules/hooks/check.sh)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [[ "$output" == "FF-4: bundle refresh started"* ]]
}

@test "bundle-refresh: a landed diff touching no stamp input does not start anything" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state modules/hooks
  stub_bgrun
  local old
  old="$(land_commit docs/readme.md)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [[ "$output" == "FF-4: bundle refresh not needed"* ]]
  [ ! -e "$STUB_BIN/bgrun-calls" ]
}

@test "bundle-refresh: a clone with no reinstall file (no bundle) is skipped" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  stub_bgrun
  local old
  old="$(land_commit flake.lock)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [[ "$output" == "FF-4: bundle refresh skipped (no "* ]]
  [ ! -e "$STUB_BIN/bgrun-calls" ]
}

@test "bundle-refresh: a bgrun failure is reported with the manual command and still exits 0" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state
  stub_bgrun 2 "bgrun: a job named x is still running"
  local old
  old="$(land_commit flake.lock)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [[ "$output" == "FF-4: bundle refresh NOT started (bgrun "* ]]
  [[ "$output" == *"still running"* ]]
  [[ "$output" == *"touch $STUB_BIN/reinstall-ran"* ]]
}

@test "bundle-refresh: bgrun absent from PATH is reported with the manual command and exits 0" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state
  local old
  old="$(land_commit flake.lock)"
  run_pg_hooks_absent --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [[ "$output" == "FF-4: bundle refresh NOT started (bgrun is not on PATH)"* ]]
  [[ "$output" == *"touch $STUB_BIN/reinstall-ran"* ]]
}

@test "bundle-refresh: an unresolvable old sha is skipped, never a failure" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state
  stub_bgrun
  run bash "$BIN" --bundle-refresh deadbeefdeadbeefdeadbeefdeadbeefdeadbeef
  [ "$status" -eq 0 ]
  [[ "$output" == "FF-4: bundle refresh skipped (cannot diff "* ]]
  [ ! -e "$STUB_BIN/bgrun-calls" ]
}

@test "bundle-refresh: a symlink current pointer is ignored (stampPaths not read)" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  make_bundle_state modules/hooks
  local common
  common="$(git rev-parse --path-format=absolute --git-common-dir)"
  rm "$common/pg-hooks/current"
  ln -s gen-1 "$common/pg-hooks/current"
  stub_bgrun
  local old
  old="$(land_commit modules/hooks/check.sh)"
  run bash "$BIN" --bundle-refresh "$old"
  [ "$status" -eq 0 ]
  [[ "$output" == "FF-4: bundle refresh not needed"* ]]
}

@test "bundle-refresh: requires an old sha" {
  run bash "$BIN" --bundle-refresh
  [ "$status" -ne 0 ]
}

@test "bundle-refresh: combined with --facts is a usage error" {
  run bash "$BIN" --facts --bundle-refresh abc
  [ "$status" -ne 0 ]
}

@test "prek-branch-diff: combined with --facts is a usage error" {
  run bash "$BIN" --facts --prek-branch-diff
  [ "$status" -ne 0 ]
}

@test "help: --help exits 0 and documents --prek-branch-diff" {
  run bash "$BIN" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"--prek-branch-diff"* ]]
}

@test "fail-safe: --facts exits nonzero when run outside a git repository" {
  local nogit_dir
  nogit_dir="$(mktemp -d)"
  cd "$nogit_dir" || return 1
  run bash "$BIN" --facts
  cd "$TEST_DIR" || true
  rm -rf "$nogit_dir"
  [ "$status" -ne 0 ]
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
