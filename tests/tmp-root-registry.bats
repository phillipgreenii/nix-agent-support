#!/usr/bin/env bats
# The REAL pg-disk-reclaimer CLI against the registry the build-tmp and
# claude-code-tmp modules render (bead pg2-rym7f, packet B). The registry files
# come from the REAL modules and the REAL registryEntries schema
# (tests/tmp-root-registry.nix, via checks.<system>.test-tmp-root-registry), with
# the roots spelled /ROOT_PLACEHOLDER/<basename>; each test points them at throwaway
# directories made with mktemp (short paths), never at a real root.
#
# Inputs (store paths of rendered registry.json files). A bare `bats tests/` has none
# and SKIPS; the nix check provides them:
#   REG_BOTH    both items
#   REG_BUILD   build-tmp only
#   REG_CLAUDE  claude-code-tmp only
bats_require_minimum_version 1.5.0

setup() {
  if [ -z "${REG_BOTH:-}" ] || [ -z "${REG_BUILD:-}" ] || [ -z "${REG_CLAUDE:-}" ]; then
    skip "rendered registries not provided; run checks.<system>.test-tmp-root-registry"
  fi
  VOL="$(mktemp -d /tmp/pgtq.XXXXXX 2>/dev/null || mktemp -d)"
  VOL="$(cd -P "$VOL" && pwd -P)"
  B="$VOL/build_tmp"
  C="$VOL/claude_code_tmp"
  mkdir -m 0700 "$B" "$C"
  : >"$B/.build-tmp-root"
  : >"$C/.claude-code-tmp-root"
  XDG="$VOL/xdg"
  mkdir -p "$XDG/pg-disk-reclaimer"
}

teardown() {
  [ -n "${VOL:-}" ] || return 0
  chmod -R u+rwx "$VOL" 2>/dev/null || true
  rm -rf "$VOL"
}

# need_ps: the sweep fails closed without ps and lsof (a darwin nix sandbox denies
# them), so the cases that sweep skip there; run them outside the sandbox too.
need_ps() {
  local out
  if ! out="$(ps -axww -o command= 2>/dev/null)" || [ -z "$out" ] ||
    ! out="$(lsof -n -w -d cwd -Fn 2>/dev/null)" || [ -z "$out" ]; then
    skip "ps or lsof is not usable here (sandbox?): the sweep fails closed without them"
  fi
}

# listed PATH: the dry run prints exactly the line "would remove: PATH".
listed() { grep -qxF -- "would remove: $1" <<<"$output"; }

# refute CMD...: fails the test when CMD succeeds (a bare `! cmd` does not trip
# errexit in bats, so it would silently pass).
refute() {
  if "$@"; then
    return 1
  fi
}

use() { sed "s#/ROOT_PLACEHOLDER#$VOL#g" "$1" >"$XDG/pg-disk-reclaimer/registry.json"; }

ts_ago() {
  local e
  e=$(($(date +%s) - $1 * 60))
  if ! date -d "@$e" +%Y%m%d%H%M.%S 2>/dev/null; then date -r "$e" +%Y%m%d%H%M.%S; fi
}
age() { find "$1" -exec touch -h -t "$(ts_ago "$2")" {} +; }

cli() { run env XDG_CONFIG_HOME="$XDG" HOME="$VOL" pg-disk-reclaimer "$@"; }

# entries of three ages in each root: 2 h and 25 h for build_tmp; 1 day, 3 days and
# 15 days for claude_code_tmp (session directories)
populate() {
  mkdir "$B/h2" "$B/h25"
  age "$B/h2" 120
  age "$B/h25" 1500
  mkdir -p "$C/claude-1/proj/d1" "$C/claude-1/proj/d3" "$C/claude-1/proj/d15"
  age "$C/claude-1" $((15 * 24 * 60))
  age "$C/claude-1/proj/d1" $((1 * 24 * 60))
  age "$C/claude-1/proj/d3" $((3 * 24 * 60))
}

@test "validate accepts each rendered registry" {
  for f in "$REG_BOTH" "$REG_BUILD" "$REG_CLAUDE"; do
    run env HOME="$VOL" pg-disk-reclaimer validate "$f"
    [ "$status" -eq 0 ]
  done
}

@test "the item is absent when its module is off: each registry holds exactly its own items" {
  [ "$(jq -r '[.[].id] | sort | join(",")' "$REG_BOTH")" = "build-tmp,claude-code-tmp" ]
  [ "$(jq -r '[.[].id] | join(",")' "$REG_BUILD")" = "build-tmp" ]
  [ "$(jq -r '[.[].id] | join(",")' "$REG_CLAUDE")" = "claude-code-tmp" ]
}

@test "level selection: --aggressiveness 1 picks the level-1 variants of both items" {
  need_ps
  use "$REG_BOTH"
  populate
  cli reclaim --aggressiveness 1 build-tmp claude-code-tmp
  [ "$status" -eq 0 ]
  listed "$B/h25"
  refute listed "$B/h2"
  listed "$C/claude-1/proj/d15"
  refute listed "$C/claude-1/proj/d3"
  refute listed "$C/claude-1/proj/d1"
}

@test "level selection: --aggressiveness 3 picks the level-3 variants of both items" {
  need_ps
  use "$REG_BOTH"
  populate
  cli reclaim --aggressiveness 3 build-tmp claude-code-tmp
  [ "$status" -eq 0 ]
  listed "$B/h25"
  listed "$B/h2"
  listed "$C/claude-1/proj/d15"
  listed "$C/claude-1/proj/d3"
  refute listed "$C/claude-1/proj/d1"
}

@test "level selection: --aggressiveness 0 selects nothing and the default run removes nothing" {
  need_ps
  use "$REG_BOTH"
  populate
  cli reclaim --aggressiveness 0 build-tmp claude-code-tmp --apply
  [[ $output != *"would remove"* ]]
  [[ $output != *"removed:"* ]]
  [ -d "$B/h25" ] && [ -d "$C/claude-1/proj/d15" ]
  cli reclaim --aggressiveness 3 build-tmp claude-code-tmp
  [ "$status" -eq 0 ]
  [ -d "$B/h25" ] && [ -d "$B/h2" ] && [ -d "$C/claude-1/proj/d15" ]
}

@test "--apply at levels 1 and 3 needs no prompt (stdin closed) and removes exactly the selected units" {
  need_ps
  use "$REG_BOTH"
  populate
  run env XDG_CONFIG_HOME="$XDG" HOME="$VOL" pg-disk-reclaimer reclaim --aggressiveness 1 build-tmp claude-code-tmp --apply </dev/null
  [ "$status" -eq 0 ]
  [ ! -e "$B/h25" ]
  [ -d "$B/h2" ]
  [ ! -e "$C/claude-1/proj/d15" ]
  [ -d "$C/claude-1/proj/d3" ]
  [[ $output == *"total reclaimed"* ]]
  run env XDG_CONFIG_HOME="$XDG" HOME="$VOL" pg-disk-reclaimer reclaim --aggressiveness 3 build-tmp claude-code-tmp --apply </dev/null
  [ "$status" -eq 0 ]
  [ ! -e "$B/h2" ]
  [ ! -e "$C/claude-1/proj/d3" ]
  [ -f "$B/.build-tmp-root" ] && [ -f "$C/.claude-code-tmp-root" ]
}

@test "naming only claude-code-tmp touches nothing under build_tmp" {
  need_ps
  use "$REG_BOTH"
  populate
  cli reclaim --aggressiveness 3 claude-code-tmp --apply
  [ "$status" -eq 0 ]
  [ -d "$B/h25" ] && [ -d "$B/h2" ]
  [ ! -e "$C/claude-1/proj/d15" ]
}

@test "a registry with only build-tmp has no claude-code-tmp item to select" {
  need_ps
  use "$REG_BUILD"
  populate
  cli reclaim --aggressiveness 1 claude-code-tmp
  [ "$status" -ne 0 ]
  [ -d "$C/claude-1/proj/d15" ]
}

@test "a root that does not exist is skipped silently, as an unmounted volume would be" {
  need_ps
  use "$REG_BOTH"
  rm -rf "$B"
  cli reclaim --aggressiveness 1 build-tmp
  [ "$status" -eq 0 ]
}

@test "a mis-set path (no marker) makes the CLI report the failure and keep going" {
  need_ps
  use "$REG_BOTH"
  populate
  rm "$B/.build-tmp-root"
  cli reclaim --aggressiveness 1 build-tmp claude-code-tmp --apply
  [ "$status" -ne 0 ]
  [ -d "$B/h25" ]
  [ ! -e "$C/claude-1/proj/d15" ]
  [[ $output == *"total reclaimed"* ]]
}
