#!/usr/bin/env bats
# The daily tmp-sweep agent's EXACT command line, end to end (bead pg2-bo3n7, packet C
# of the build_tmp design). The agent script comes from the REAL tmp-sweep module
# (tests/tmp-sweep.nix, via checks.<system>.test-tmp-sweep) and holds the absolute store
# path of the REAL pg-disk-reclaimer; the registry is the REAL one the build-tmp and
# claude-code-tmp modules render. Each test runs that script exactly as launchd would
# (a bare environment with only HOME and XDG_CONFIG_HOME set) against a fixture registry
# whose roots are throwaway directories from mktemp (short paths). The real registry
# path (~/.config/pg-disk-reclaimer/registry.json) is never read or written: HOME and
# XDG_CONFIG_HOME both point into the fixture.
#
# Inputs (store paths). A bare `bats tests/` has none and SKIPS; the nix check provides:
#   SCRIPT_BOTH / SCRIPT_BUILD / SCRIPT_CLAUDE  rendered agent scripts
#   REG_BOTH / REG_BUILD / REG_CLAUDE           the matching registry.json files
bats_require_minimum_version 1.5.0

setup() {
  if [ -z "${SCRIPT_BOTH:-}" ] || [ -z "${SCRIPT_BUILD:-}" ] || [ -z "${SCRIPT_CLAUDE:-}" ] ||
    [ -z "${REG_BOTH:-}" ] || [ -z "${REG_BUILD:-}" ] || [ -z "${REG_CLAUDE:-}" ]; then
    skip "rendered scripts and registries not provided; run checks.<system>.test-tmp-sweep"
  fi
  VOL="$(mktemp -d /tmp/pgts.XXXXXX 2>/dev/null || mktemp -d)"
  VOL="$(cd -P "$VOL" && pwd -P)"
  B="$VOL/build_tmp"
  C="$VOL/claude_code_tmp"
  mkdir -m 0700 "$B" "$C"
  : >"$B/.build-tmp-root"
  : >"$C/.claude-code-tmp-root"
  XDG="$VOL/xdg"
  mkdir -p "$XDG/pg-disk-reclaimer"
  PIDS="$VOL/pids"
  : >"$PIDS"
}

teardown() {
  [ -n "${VOL:-}" ] || return 0
  if [ -s "$PIDS" ]; then
    # shellcheck disable=SC2046
    kill $(cat "$PIDS") 2>/dev/null || true
  fi
  chmod -R u+rwx "$VOL" 2>/dev/null || true
  rm -rf "$VOL"
}

# need_ps: the sweep fails closed without ps and lsof (a darwin nix sandbox may deny
# them), so the cases that sweep skip there; run them outside the sandbox too.
need_ps() {
  local out
  if ! out="$(ps -axww -o command= 2>/dev/null)" || [ -z "$out" ] ||
    ! out="$(lsof -n -w -d cwd -Fn 2>/dev/null)" || [ -z "$out" ]; then
    skip "ps or lsof is not usable here (sandbox?): the sweep fails closed without them"
  fi
}

use() { sed "s#/ROOT_PLACEHOLDER#$VOL#g" "$1" >"$XDG/pg-disk-reclaimer/registry.json"; }

ts_ago() {
  local e
  e=$(($(date +%s) - $1 * 60))
  if ! date -d "@$e" +%Y%m%d%H%M.%S 2>/dev/null; then date -r "$e" +%Y%m%d%H%M.%S; fi
}
age() { find "$1" -exec touch -h -t "$(ts_ago "$2")" {} +; }

# spawn_session UUID: a live process whose argv carries `claude --session-id UUID`
# (the shape of an interactive session; the live-session guard reads ps).
spawn_session() {
  (exec bash -c 'sleep 300; :' claude --session-id "$1") >/dev/null 2>&1 &
  echo $! >>"$PIDS"
  for _ in $(seq 1 300); do
    [[ "$(ps -axww -o command= 2>/dev/null)" == *"--session-id $1"* ]] && return 0
  done
  return 1
}

# run_agent SCRIPT: the agent as launchd runs it, a bare environment holding only
# HOME, XDG_CONFIG_HOME and the tools' PATH.
run_agent() {
  run env -i PATH="$PATH" HOME="$VOL" XDG_CONFIG_HOME="$XDG" bash "$1" </dev/null
}

live="0000aaaa-0000-4000-8000-00000000aaaa"
idle="0000bbbb-0000-4000-8000-00000000bbbb"
fresh="0000cccc-0000-4000-8000-00000000cccc"

# Entries: build_tmp idle 30 h (past the 24 h cutoff) and fresh 1 h; claude_code_tmp
# session directories idle 15 days (past the 14 day cutoff), fresh 1 day, and live
# (idle 15 days but its uuid is in a running process's argv).
populate() {
  mkdir "$B/idle" "$B/fresh"
  : >"$B/idle/f"
  : >"$B/fresh/f"
  age "$B/idle" 1800
  age "$B/fresh" 60
  mkdir -p "$C/claude-1/proj/$idle/scratchpad" "$C/claude-1/proj/$fresh/scratchpad" "$C/claude-1/proj/$live/scratchpad"
  : >"$C/claude-1/proj/$idle/scratchpad/f"
  : >"$C/claude-1/proj/$fresh/scratchpad/f"
  : >"$C/claude-1/proj/$live/scratchpad/f"
  age "$C/claude-1" $((15 * 24 * 60))
  age "$C/claude-1/proj/$fresh" $((1 * 24 * 60))
}

@test "the rendered scripts are the exact command lines, one per enabled-roots combination" {
  [ "$(grep -c . "$SCRIPT_BOTH")" -eq 1 ]
  grep -qE '^exec /nix/store/[^ ]+/bin/pg-disk-reclaimer reclaim --aggressiveness 1 build-tmp claude-code-tmp --apply$' "$SCRIPT_BOTH"
  grep -qE '^exec /nix/store/[^ ]+/bin/pg-disk-reclaimer reclaim --aggressiveness 1 build-tmp --apply$' "$SCRIPT_BUILD"
  grep -qE '^exec /nix/store/[^ ]+/bin/pg-disk-reclaimer reclaim --aggressiveness 1 claude-code-tmp --apply$' "$SCRIPT_CLAUDE"
}

@test "both roots: idle build-tmp entry and idle claude-code-tmp session removed; fresh and live kept" {
  need_ps
  use "$REG_BOTH"
  populate
  spawn_session "$live"
  run_agent "$SCRIPT_BOTH"
  [ "$status" -eq 0 ]
  # build_tmp: the idle entry goes, the fresh one stays
  [ ! -e "$B/idle" ]
  [ -d "$B/fresh" ] && [ -f "$B/fresh/f" ]
  # claude_code_tmp: the idle session goes, the fresh and the live ones stay
  [ ! -e "$C/claude-1/proj/$idle" ]
  [ -f "$C/claude-1/proj/$fresh/scratchpad/f" ]
  [ -f "$C/claude-1/proj/$live/scratchpad/f" ]
  # each root's marker survives (the root guard needs it on every later run)
  [ -f "$B/.build-tmp-root" ] && [ -f "$C/.claude-code-tmp-root" ]
  [[ $output == *"total reclaimed"* ]]
}

@test "level 1 only: a build_tmp entry idle 2 h (past the level 3 cutoff, not level 1) is kept" {
  need_ps
  use "$REG_BOTH"
  mkdir "$B/two-hours"
  age "$B/two-hours" 120
  mkdir -p "$C/claude-1/proj/$idle"
  age "$C/claude-1" $((3 * 24 * 60))
  run_agent "$SCRIPT_BOTH"
  [ "$status" -eq 0 ]
  [ -d "$B/two-hours" ]
  [ -d "$C/claude-1/proj/$idle" ]
}

@test "only build-tmp enabled: the script touches nothing under claude_code_tmp" {
  need_ps
  use "$REG_BUILD"
  populate
  run_agent "$SCRIPT_BUILD"
  [ "$status" -eq 0 ]
  [ ! -e "$B/idle" ] && [ -d "$B/fresh" ]
  [ -d "$C/claude-1/proj/$idle" ]
}

@test "only claude-code-tmp enabled: the script touches nothing under build_tmp" {
  need_ps
  use "$REG_CLAUDE"
  populate
  run_agent "$SCRIPT_CLAUDE"
  [ "$status" -eq 0 ]
  [ ! -e "$C/claude-1/proj/$idle" ]
  [ -d "$B/idle" ] && [ -d "$B/fresh" ]
}

@test "the registry is read from XDG_CONFIG_HOME: an empty config directory removes nothing" {
  need_ps
  populate
  # no registry in the fixture XDG directory (negative control for the tests above)
  run_agent "$SCRIPT_BOTH"
  [ "$status" -ne 0 ]
  [ -d "$B/idle" ]
  [ -d "$C/claude-1/proj/$idle" ]
}

@test "a missing root (unmounted volume) is skipped and the other root is still swept" {
  need_ps
  use "$REG_BOTH"
  populate
  rm -rf "$B"
  run_agent "$SCRIPT_BOTH"
  [ "$status" -eq 0 ]
  [ ! -e "$C/claude-1/proj/$idle" ]
}
