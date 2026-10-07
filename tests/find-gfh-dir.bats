#!/usr/bin/env bats
# bats file_tags=type:unit
# Covers tests/support/find-gfh-dir.sh, the resolver the bats suites use to
# locate git-fixture-harness.bash. The script is copied OUTSIDE any git repo so
# the git-common-dir probe cannot find the real nix-repo-base; each case drives
# discovery purely through GFH_LIB / TEST_SUPPORT / PN_WORKSPACE_ROOT.

setup() {
  RESOLVER="$BATS_TEST_TMPDIR/find-gfh-dir.sh"
  cp "$BATS_TEST_DIRNAME/support/find-gfh-dir.sh" "$RESOLVER"
  WS="$BATS_TEST_TMPDIR/ws"
  mkdir -p "$WS"
  unset GFH_LIB TEST_SUPPORT PN_WORKSPACE_ROOT
  export GIT_CEILING_DIRECTORIES="$BATS_TEST_TMPDIR"
}

_make_harness() {
  mkdir -p "$WS/$1/lib/scripts"
  : >"$WS/$1/lib/scripts/git-fixture-harness.bash"
}

@test "finds the nix-repo-base sibling via PN_WORKSPACE_ROOT" {
  _make_harness nix-repo-base
  PN_WORKSPACE_ROOT="$WS" run "$RESOLVER"
  [ "$status" -eq 0 ]
  [ "$output" = "$(cd "$WS/nix-repo-base/lib/scripts" && pwd -P)" ]
}

@test "falls back to the legacy phillipg-nix-repo-base name" {
  _make_harness phillipg-nix-repo-base
  PN_WORKSPACE_ROOT="$WS" run "$RESOLVER"
  [ "$status" -eq 0 ]
  [ "$output" = "$(cd "$WS/phillipg-nix-repo-base/lib/scripts" && pwd -P)" ]
}

@test "prefers nix-repo-base when both names exist" {
  _make_harness nix-repo-base
  _make_harness phillipg-nix-repo-base
  PN_WORKSPACE_ROOT="$WS" run "$RESOLVER"
  [ "$status" -eq 0 ]
  [[ $output == */nix-repo-base/lib/scripts ]]
}

@test "GFH_LIB wins over the workspace search" {
  _make_harness nix-repo-base
  mkdir -p "$BATS_TEST_TMPDIR/custom"
  : >"$BATS_TEST_TMPDIR/custom/git-fixture-harness.bash"
  GFH_LIB="$BATS_TEST_TMPDIR/custom/git-fixture-harness.bash" PN_WORKSPACE_ROOT="$WS" run "$RESOLVER"
  [ "$status" -eq 0 ]
  [ "$output" = "$BATS_TEST_TMPDIR/custom" ]
}

@test "result does not depend on the caller's cwd" {
  _make_harness nix-repo-base
  cd /
  PN_WORKSPACE_ROOT="$WS" run "$RESOLVER"
  [ "$status" -eq 0 ]
  [[ $output == */nix-repo-base/lib/scripts ]]
}

@test "exits 1 and lists every tried path when nothing is found" {
  PN_WORKSPACE_ROOT="$WS" run "$RESOLVER"
  [ "$status" -eq 1 ]
  [[ $output == *"cannot locate git-fixture-harness.bash"* ]]
  [[ $output == *"$WS/nix-repo-base/lib/scripts"* ]]
  [[ $output == *"$WS/phillipg-nix-repo-base/lib/scripts"* ]]
}
