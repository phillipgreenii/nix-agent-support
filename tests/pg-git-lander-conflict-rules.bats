#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-tool regression guard for the [[conflict]] rules in this repo's
# .pg-git-lander.toml: the REAL pg-git-lander land, REAL git, REAL `nix flake lock`
# / `nix flake update` and REAL `go mod tidy`, against throwaway fixture repos built
# under BATS_TEST_TMPDIR. Nothing is mocked. The rules under test are cut out of the
# repo's own file (everything from the first [[conflict]] header on), so a change to
# the file is what this suite measures.
#
# Hermetic: the fixture flake inputs are local `git+file://` repos and the Go
# dependencies come from a `file://` GOPROXY built in the fixture, so there is no
# network. It does need a nix daemon that can evaluate flakes and a pg-git-lander on
# PATH, so it skips itself inside the nix build sandbox and is NOT a unit test (it
# is selected only by `--filter-tags type:integration`; the unit tier and pre-land
# never run it). Opt in with `nix run .#pg-git-lander-conflict-rules-tests`.
#
# Findings recorded by the flake.lock scenarios (nix 2.34, 2026-11-12):
#   - a bare `nix flake lock` keeps an already-pinned input at its old revision (see the
#     control test), so the rule runs `nix flake update <inputs>` for each input the
#     branch's lock changed against the common ancestor (bumped, repointed or added), then
#     a `nix flake lock` to fill anything missing. A full `nix flake update` was not
#     needed.

SUITE="pg-git-lander-conflict-rules"
# The directory of the Go module the go.sum scenario builds (must match the rule glob).
GO_DIR="packages/app"

setup() {
  if [ -n "${IN_NIX_BUILD:-}" ] || [ -n "${NIX_BUILD_TOP:-}" ]; then
    skip "inside the nix build sandbox: no nested nix evaluation"
  fi
  # GFH_LIB is the one required file: no filesystem search. Checked here, not at file
  # load, so test discovery works without it.
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), via 'nix run .#pg-git-lander-conflict-rules-tests', or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090
  source "$GFH_LIB"
  local tool
  for tool in nix git go jq zip pg-git-lander; do
    # An opt-in suite that cannot run must fail, never pass vacuously.
    if ! command -v "$tool" >/dev/null 2>&1; then
      echo "$tool not on PATH: this suite drives the real tools (nix, git, go, jq, zip, pg-git-lander)" >&2
      return 1
    fi
  done

  REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  gfh_setup "$SUITE"
  FX="$GFH_WORK"
  R="$GFH_REPO"

  # Isolated lander, nix and go configuration (set after gfh_setup, which resets the env).
  export XDG_CONFIG_HOME="$FX/xdg" XDG_STATE_HOME="$FX/state" XDG_CACHE_HOME="$FX/cache"
  mkdir -p "$XDG_CONFIG_HOME/pg-git-lander" "$XDG_STATE_HOME" "$XDG_CACHE_HOME"
  export NIX_CONFIG=$'experimental-features = nix-command flakes\nwarn-dirty = false'
  git config --file "$HOME/.gitconfig" protocol.file.allow always
  export GOFLAGS="-mod=mod -modcacherw"
  export GOPROXY="file://$FX/proxy"
  export GONOSUMDB='*' GOSUMDB=off GOTOOLCHAIN=local GOWORK=off GOENV=off
  export GOCACHE="$FX/gocache" GOPATH="$FX/gopath" GOMODCACHE="$FX/gomodcache"

  # The rules under test, taken from the repo's own file.
  awk '/^\[\[conflict\]\]/ { p = 1 } p' "$REPO_ROOT/.pg-git-lander.toml" >"$FX/rules.toml"
  [ -s "$FX/rules.toml" ]
}

teardown() {
  # gfh_teardown exists only once setup got as far as sourcing the harness.
  if declare -F gfh_teardown >/dev/null; then gfh_teardown; fi
}

# --- helpers ---------------------------------------------------------------

commit_all() { git -C "$1" add -A && git -C "$1" commit -q -m "$2"; }

# init_fixture_repo: R on main holding the rules as its repo file, trusted via XDG.
init_fixture_repo() {
  cp "$FX/rules.toml" "$R/.pg-git-lander.toml"
  cat >"$XDG_CONFIG_HOME/pg-git-lander/config.toml" <<EOF
[[repo]]
root = "$R"
trust_repo_config = true
EOF
}

# land_branch BRANCH: lands from a source worktree; sets $output / $status via run.
land_branch() {
  git -C "$R" worktree add -q -b "$1" "$FX/wt-$1" 2>/dev/null || git -C "$R" worktree add -q "$FX/wt-$1" "$1"
}

do_land() {
  run pg-git-lander land "$1" --repo "$R" --source-worktree "$FX/wt-$1" --wait 30s --json
}

# no_markers FILE: fails when FILE still holds a conflict block.
no_markers() {
  if grep -qE '^(<{7}|>{7}) ' "$1"; then return 1; fi
}

# assert_resolved PATH: the lander really hit a conflict on PATH and a repo-file rule resolved it.
assert_resolved() {
  echo "$output" | jq -e --arg p "$1" '.outcome == "landed" and .stats.conflict_rounds >= 1
    and ([.resolved[] | select(.path == $p and (.rule | startswith("repo-file:")) and .result == "resolved")] | length) >= 1' >/dev/null
}

# bump_a: a new upstream commit in input a, so a lock update moves a's pin.
bump_a() { git -C "$FX/a" commit -q --allow-empty -m "a bump $RANDOM"; }

# --- flake.lock ------------------------------------------------------------

# mk_input NAME: a flake input repo with one commit.
mk_input() {
  gfh_init_repo "$FX/$1" "$SUITE"
  printf '{ outputs = _: { }; }\n' >"$FX/$1/flake.nix"
  commit_all "$FX/$1" "$1 v1"
}

write_root_flake() { # args: extra input lines placed after `a`, and at the end
  cat >"${D:-$R}/flake.nix" <<EOF
{
  description = "fixture";

  inputs = {
    a.url = "git+file://$FX/${A_SRC:-a}";
$1

    # spacer 1
    # spacer 2
    # spacer 3
    # spacer 4
$2
  };

  outputs = _: { };
}
EOF
}

lock_root() { git -C "$1" add -A && (cd "$1" && nix flake lock >/dev/null 2>&1); }

flake_fixture() {
  mk_input a
  mk_input a2
  mk_input b
  mk_input c
  init_fixture_repo
  write_root_flake "" ""
  lock_root "$R"
  commit_all "$R" base
}

assert_lock_clean() {
  no_markers "$R/flake.lock"
  jq -e . "$R/flake.lock" >/dev/null
  # the lock is up to date with flake.nix: nix would need no change
  (cd "$R" && nix flake lock --no-update-lock-file >/dev/null 2>&1)
}

@test "flake.lock conflict: both sides add an input; the lock is regenerated with every input" {
  flake_fixture
  git -C "$R" branch feat
  land_branch feat
  # main adds input c and bumps a (flake.nix and flake.lock)
  bump_a
  write_root_flake "" "    c.url = \"git+file://$FX/c\";"
  lock_root "$R"
  (cd "$R" && nix flake update a >/dev/null 2>&1)
  commit_all "$R" "main adds c and bumps a"
  bump_a
  new_rev="$(git -C "$FX/a" rev-parse HEAD)"
  # the branch adds input b
  D="$FX/wt-feat" write_root_flake "    b.url = \"git+file://$FX/b\";" ""
  lock_root "$FX/wt-feat"
  (cd "$FX/wt-feat" && nix flake update a >/dev/null 2>&1)
  commit_all "$FX/wt-feat" "branch adds b and bumps a"

  do_land feat
  [ "$status" -eq 0 ]
  assert_resolved flake.lock
  git -C "$R" reset -q --hard main
  assert_lock_clean
  [ "$(jq -r '.nodes.root.inputs | keys | join(",")' "$R/flake.lock")" = "a,b,c" ]
  [ "$(jq -r '.nodes.a.locked.rev' "$R/flake.lock")" = "$new_rev" ]
}

@test "flake.lock conflict: an input whose url the branch changed is re-resolved (stale entry)" {
  flake_fixture
  git -C "$R" branch feat
  land_branch feat
  bump_a
  write_root_flake "" "    c.url = \"git+file://$FX/c\";"
  lock_root "$R"
  (cd "$R" && nix flake update a >/dev/null 2>&1)
  commit_all "$R" "main adds c and bumps a"
  D="$FX/wt-feat" A_SRC=a2 write_root_flake "" ""
  lock_root "$FX/wt-feat"
  commit_all "$FX/wt-feat" "branch repoints a"

  do_land feat
  [ "$status" -eq 0 ]
  assert_resolved flake.lock
  git -C "$R" reset -q --hard main
  assert_lock_clean
  jq -e '.nodes.a.original.url | endswith("/a2")' "$R/flake.lock" >/dev/null
  jq -e '.nodes.a.locked.url | endswith("/a2")' "$R/flake.lock" >/dev/null
}

@test "flake.lock conflict: a branch that only bumps an input keeps its bump" {
  flake_fixture
  git -C "$R" branch feat
  land_branch feat
  # main adds c and bumps a; the branch (flake.nix untouched) bumps a to a later revision
  bump_a
  write_root_flake "" "    c.url = \"git+file://$FX/c\";"
  lock_root "$R"
  (cd "$R" && nix flake update a >/dev/null 2>&1)
  commit_all "$R" "main adds c and bumps a"
  bump_a
  new_rev="$(git -C "$FX/a" rev-parse HEAD)"
  (cd "$FX/wt-feat" && nix flake update a >/dev/null 2>&1)
  commit_all "$FX/wt-feat" "branch bumps a"

  do_land feat
  [ "$status" -eq 0 ]
  assert_resolved flake.lock
  git -C "$R" reset -q --hard main
  assert_lock_clean
  [ "$(jq -r '.nodes.a.locked.rev' "$R/flake.lock")" = "$new_rev" ]
  [ "$(jq -r '.nodes.root.inputs | keys | join(",")' "$R/flake.lock")" = "a,c" ]
}

# Negative control for the rule's choice of `nix flake update`: a bare `nix flake lock`
# keeps an already-pinned input at its old revision, so it alone cannot regenerate a stale lock.
@test "control: a bare nix flake lock leaves a stale pin; nix flake update moves it" {
  flake_fixture
  old_rev="$(jq -r '.nodes.a.locked.rev' "$R/flake.lock")"
  bump_a
  new_rev="$(git -C "$FX/a" rev-parse HEAD)"
  [ "$old_rev" != "$new_rev" ]
  (cd "$R" && nix flake lock >/dev/null 2>&1)
  [ "$(jq -r '.nodes.a.locked.rev' "$R/flake.lock")" = "$old_rev" ]
  (cd "$R" && nix flake update a >/dev/null 2>&1)
  [ "$(jq -r '.nodes.a.locked.rev' "$R/flake.lock")" = "$new_rev" ]
}

# --- go.sum ----------------------------------------------------------------

# mk_go_dep NAME: a module example.com/NAME v1.0.0 in the fixture file:// GOPROXY.
mk_go_dep() {
  local d="$FX/proxy/example.com/$1/@v" src="$FX/gosrc/example.com/$1@v1.0.0"
  mkdir -p "$d" "$src"
  printf 'module example.com/%s\n\ngo 1.21\n' "$1" >"$src/go.mod"
  printf 'package %s\n' "$1" >"$src/$1.go"
  cp "$src/go.mod" "$d/v1.0.0.mod"
  (cd "$FX/gosrc" && zip -q -r -D "$d/v1.0.0.zip" "example.com/$1@v1.0.0")
  printf '{"Version":"v1.0.0","Time":"2020-01-01T00:00:00Z"}\n' >"$d/v1.0.0.info"
  printf 'v1.0.0\n' >"$d/list"
}

write_go_module() { # $1 = extra require (top), $2 = extra require (bottom), $3 = extra import, $4 = bottom import
  mkdir -p "$R/$GO_DIR"
  cat >"$R/$GO_DIR/go.mod" <<EOF
module example.com/app

go 1.21
$1
require example.com/dep1 v1.0.0

// spacer 1
// spacer 2
// spacer 3
// spacer 4
$2
EOF
  cat >"$R/$GO_DIR/main.go" <<EOF
package main

import _ "example.com/dep1"
$3

// spacer 1
// spacer 2
// spacer 3
// spacer 4
$4

func main() {}
EOF
}

@test "go.sum conflict: both sides add a dependency; go.sum is regenerated by go mod tidy" {
  grep -q 'go.sum' "$FX/rules.toml" || skip "this repo's file has no go.sum rule"
  mk_go_dep dep1
  mk_go_dep dep2
  mk_go_dep dep3
  init_fixture_repo
  write_go_module "" "" "" ""
  (cd "$R/$GO_DIR" && go mod tidy >/dev/null 2>&1)
  commit_all "$R" base
  git -C "$R" branch feat
  land_branch feat
  # main adds dep2
  write_go_module $'\nrequire example.com/dep2 v1.0.0\n' "" 'import _ "example.com/dep2"' ""
  (cd "$R/$GO_DIR" && go mod tidy >/dev/null 2>&1)
  commit_all "$R" "main adds dep2"
  # the branch adds dep3
  R_SAVE="$R"
  R="$FX/wt-feat"
  write_go_module "" $'require example.com/dep3 v1.0.0\n' "" 'import _ "example.com/dep3"'
  (cd "$R/$GO_DIR" && go mod tidy >/dev/null 2>&1)
  commit_all "$R" "branch adds dep3"
  R="$R_SAVE"

  do_land feat
  [ "$status" -eq 0 ]
  assert_resolved "$GO_DIR/go.sum"
  git -C "$R" reset -q --hard main
  no_markers "$R/$GO_DIR/go.sum"
  for d in dep1 dep2 dep3; do grep -q "example.com/$d v1.0.0 h1:" "$R/$GO_DIR/go.sum"; done
  (cd "$R/$GO_DIR" && go mod verify >/dev/null 2>&1 && go build ./... >/dev/null 2>&1)
}
