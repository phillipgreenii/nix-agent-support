#!/usr/bin/env bats
# Behaviour of the activation step rendered by the build-tmp and claude-code-tmp
# home-manager modules (bead pg2-2qirl, packet A): the script TEXT that
# home-manager runs after `writeBoundary`, run here against a fixture parent
# directory under `bash -euo pipefail` (home-manager's own strictness). The text
# comes from the REAL modules (tests/tmp-root-modules.nix, via
# checks.<system>.test-tmp-root-modules); the root is spelled
# /ROOT_PLACEHOLDER/<basename> there.
#
# Inputs: ACTIVATION_BUILD and ACTIVATION_CLAUDE, the rendered script files. A
# bare `bats tests/` has neither and SKIPS; the nix check provides them and
# fails on any skip.

setup() {
  if [ -z "${ACTIVATION_BUILD:-}" ] || [ -z "${ACTIVATION_CLAUDE:-}" ]; then
    skip "rendered activation scripts not provided; run checks.<system>.test-tmp-root-modules"
  fi
  VOL="$(mktemp -d "${TMPDIR:-/tmp}/pgta.XXXXXX")"
  # name:basename:marker for each module under test
  CASES="build:build_tmp:.build-tmp-root claude:claude_code_tmp:.claude-code-tmp-root"
}

teardown() {
  [ -n "${VOL:-}" ] || return 0
  chmod -R u+rwx "$VOL" 2>/dev/null || true
  rm -rf "$VOL"
}

script_for() { # name -> rendered script path for $VOL
  case "$1" in
    build) src="$ACTIVATION_BUILD" ;;
    claude) src="$ACTIVATION_CLAUDE" ;;
  esac
  sed "s#/ROOT_PLACEHOLDER#$VOL#g" "$src" >"$VOL/act-$1.sh"
  printf '%s' "$VOL/act-$1.sh"
}

# run_act NAME [DRY_RUN_CMD]: run the script as home-manager would, DRY_RUN_CMD
# empty in a real apply.
run_act() {
  env -i PATH="$PATH" DRY_RUN_CMD="${2:-}" bash -euo pipefail "$(script_for "$1")"
}

mode_of() { ls -ld "$1" | cut -c1-10; }

@test "creates the directory with mode 0700 and the marker" {
  for c in $CASES; do
    IFS=: read -r name base marker <<<"$c"
    run run_act "$name"
    [ "$status" -eq 0 ]
    [ -d "$VOL/$base" ]
    [ ! -L "$VOL/$base" ]
    [ "$(mode_of "$VOL/$base")" = "drwx------" ]
    [ -f "$VOL/$base/$marker" ]
  done
}

@test "a second run is idempotent and re-applies 0700 to a loosened directory" {
  for c in $CASES; do
    IFS=: read -r name base marker <<<"$c"
    run run_act "$name"
    [ "$status" -eq 0 ]
    echo keep >"$VOL/$base/payload"
    chmod 0755 "$VOL/$base"
    run run_act "$name"
    [ "$status" -eq 0 ]
    [ "$(mode_of "$VOL/$base")" = "drwx------" ]
    [ -f "$VOL/$base/$marker" ]
    [ "$(cat "$VOL/$base/payload")" = keep ]
  done
}

@test "a symlink at the target is refused and left untouched" {
  mkdir -m 0755 "$VOL/elsewhere"
  for c in $CASES; do
    IFS=: read -r name base marker <<<"$c"
    ln -s "$VOL/elsewhere" "$VOL/$base"
    run run_act "$name"
    [ "$status" -eq 0 ]
    [[ "$output" == *"refusing"* ]]
    [ -L "$VOL/$base" ]
    [ ! -e "$VOL/elsewhere/$marker" ]
    [ "$(mode_of "$VOL/elsewhere")" = "drwxr-xr-x" ]
  done
}

@test "a missing parent directory prints a skip and exits 0 without creating it" {
  for c in $CASES; do
    IFS=: read -r name base marker <<<"$c"
    # the fixture volume's mount point is absent: nothing may be created
    gone="$VOL/novol"
    case "$name" in build) src="$ACTIVATION_BUILD" ;; claude) src="$ACTIVATION_CLAUDE" ;; esac
    sed "s#/ROOT_PLACEHOLDER#$gone#g" "$src" >"$VOL/act-missing-$name.sh"
    run env -i PATH="$PATH" DRY_RUN_CMD= bash -euo pipefail "$VOL/act-missing-$name.sh"
    [ "$status" -eq 0 ]
    [[ "$output" == *"skipping"* ]]
    [ ! -e "$gone" ]
  done
}

@test "a failure to create prints a skip and still exits 0" {
  if [ "$(id -u)" -eq 0 ]; then skip "root ignores directory modes"; fi
  chmod 0500 "$VOL"
  for c in $CASES; do
    IFS=: read -r name base marker <<<"$c"
    run run_act_ro "$name"
    [ "$status" -eq 0 ]
    [[ "$output" == *"skipped"* ]]
    [ ! -e "$VOL/$base" ]
  done
}

# the script file itself lives outside the read-only fixture parent
run_act_ro() {
  local d
  d="$(mktemp -d "${TMPDIR:-/tmp}/pgta-ro.XXXXXX")"
  case "$1" in build) src="$ACTIVATION_BUILD" ;; claude) src="$ACTIVATION_CLAUDE" ;; esac
  sed "s#/ROOT_PLACEHOLDER#$VOL#g" "$src" >"$d/act.sh"
  env -i PATH="$PATH" DRY_RUN_CMD= bash -euo pipefail "$d/act.sh"
  local rc=$?
  rm -rf "$d"
  return $rc
}

@test "with DRY_RUN_CMD=echo nothing is created" {
  for c in $CASES; do
    IFS=: read -r name base marker <<<"$c"
    run run_act "$name" echo
    [ "$status" -eq 0 ]
    [[ "$output" == *"mkdir -p"* ]]
    [ ! -e "$VOL/$base" ]
  done
}
