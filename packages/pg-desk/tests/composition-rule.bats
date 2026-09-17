#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Tagged `type:integration` (phillipg-nix-repo-base's pg-test-runner label
# registry -- its CLAUDE.md "Go tests" section / modules/pg-test-runner):
# this suite needs a real nix-built pg-desk package plus a wrapProgram-made
# stub-wrapped copy of it, so it MUST NOT run under the commit-time
# `run-unit-tests` hook (pg-test-runner --labels unit, no nix). It runs only
# as this repo's own checks.<system>.test-pg-desk-composition-rule flake
# check (flake.nix), whose builder exports $PG_DESK_STUB_WRAPPED and
# $PG_DESK_UNWRAPPED before invoking `bats` directly (not through
# pg-test-runner) -- see that check's own comment for how those two
# binaries are built.
#
# Composition-rule smoke test (docket pg2-2j5ac.32, Phase 9, packet 10):
# proves the NIX WRAPPER -- not ambient PATH -- is what supplies pg-connector
# to the built pg-desk binary (D10: packages/pg-desk execs no binary other
# than pg-connector and the operator-configured browser, directly or
# transitively). Complements cmd/pg-desk/composition_test.go's chokepoint
# test, which runs PRE-WRAP (a source-level scan for any literal
# exec.Command binary name other than "pg-connector"); this test runs
# POST-WRAP, against the actual nix-built artifact -- the negative control
# this repo's own CLAUDE.md "pg-router config testing trap" note already
# establishes the pattern for (run the unwrapped `.<name>-wrapped` binary
# under `env -i` and confirm it fails without the wrapper's PATH injection).
#
# $PG_DESK_STUB_WRAPPED and $PG_DESK_UNWRAPPED are exported by this check's
# own builder (flake.nix's test-pg-desk-composition-rule) before `bats` is
# invoked:
#   PG_DESK_STUB_WRAPPED -- a COPY of the real package's unwrapped
#     .pg-desk-wrapped binary, wrapped via the exact SAME wrapProgram
#     `--prefix PATH :` mechanism packages/pg-desk/default.nix uses for the
#     real pg-connector, but pointed at a STUB pg-connector that answers
#     only `pr show` -- proving the WRAPPING MECHANISM itself, without
#     depending on a real, configured pg-connector backend (which `pr show`
#     against no registered backend cannot satisfy in a smoke test).
#   PG_DESK_UNWRAPPED -- the real unwrapped .pg-desk-wrapped binary, with NO
#     wrapper at all (negative control).
#
# Both binaries are run under `env -i` with no inherited PATH at all, so
# a pass on PG_DESK_STUB_WRAPPED can only be explained by the wrapper's own
# PATH injection -- never ambient shell PATH -- and a pass on
# PG_DESK_UNWRAPPED would disprove that the wrapper is doing anything.

setup() {
  WORK_DIR="$(mktemp -d)"
  export XDG_STATE_HOME="$WORK_DIR/state"
  CONFIG_FILE="$WORK_DIR/config.yaml"
  cat > "$CONFIG_FILE" <<'EOF'
self_login: phillipgreenii
repos:
  - remote: phillipgreenii/example-repo
EOF
  export CONFIG_FILE
}

teardown() {
  rm -rf "$WORK_DIR"
}

@test "nix-wrapped pg-desk resolves pg-connector via the wrapper's PATH injection, not ambient PATH" {
  run env -i PG_DESK_CONFIG="$CONFIG_FILE" XDG_STATE_HOME="$XDG_STATE_HOME" \
    "$PG_DESK_STUB_WRAPPED" run pr 1
  echo "output: $output"
  [ "$status" -eq 0 ]
}

@test "unwrapped pg-desk binary fails under env -i with no pg-connector on PATH" {
  run env -i PG_DESK_CONFIG="$CONFIG_FILE" XDG_STATE_HOME="$XDG_STATE_HOME" \
    "$PG_DESK_UNWRAPPED" run pr 1
  echo "output: $output"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pg-connector"* ]]
}
