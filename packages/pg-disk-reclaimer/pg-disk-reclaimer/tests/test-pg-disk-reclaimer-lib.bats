#!/usr/bin/env bats
# Unit tests for pg-disk-reclaimer's core subcommand functions
# (pg-disk-reclaimer.bash). list, validate, and reclaim are all now
# implemented (beads pg2-txxyj.4/.5/.6) -- only the scaffold task
# (pg2-txxyj.1) ever left them as stubs.
# The registry loading + schema validation engine (pgdr_default_registry_path
# / pgdr_validate_registry / pgdr_read_registry) is exercised below against
# fixtures under tests/fixtures/ (bead pg2-txxyj.2). The variant-selection
# algorithm (pgdr_select_variants) is exercised against
# tests/fixtures/selection.json (bead pg2-txxyj.3). cmd_list (bead
# pg2-txxyj.4) is exercised against tests/fixtures/list.json, whose
# displayCommand values are side-effect-free `echo` stubs so the table's
# size column never touches the real filesystem. cmd_validate and its
# best-effort command-existence check (pgdr_command_exists /
# pgdr_validate_commands_exist, bead pg2-txxyj.5) are exercised against
# tests/fixtures/command-exists.json and tests/fixtures/command-missing.json,
# plus the shared schema fixtures above. cmd_reclaim and pgdr_confirm (bead
# pg2-txxyj.6) are exercised against tests/fixtures/reclaim.json, always
# with pgdr_confirm overridden -- never the real /dev/tty-reading
# implementation (see its doc comment in pg-disk-reclaimer.bash for why).

# setup_file/teardown_file (run exactly ONCE for this whole file, unlike
# setup()/teardown() below which run per-test): cmd_list/cmd_reclaim now
# guard on each item's registry `path` actually existing on disk (this bug
# fix) -- create every synthetic path used by tests/fixtures/list.json,
# list-resilience.json, and reclaim.json so those existing tests keep
# exercising cmd_list/cmd_reclaim's real behavior instead of silently
# having every item skipped by the new guard. reclaim.json's
# low-item/high-item/failing-item paths are included here too even though
# cmd_reclaim's own guard logic is a separate follow-up bead -- this keeps
# the one shared fixture-path fix in a single task.
# tests/fixtures/selection.json's paths (/tmp/info-only, /tmp/multi,
# /tmp/single) are deliberately NOT created here: that fixture is
# exercised only via pgdr_select_variants directly, never through
# cmd_list/cmd_reclaim, so the new guard never applies to it.
#
# Deliberately setup_file/teardown_file, NOT per-test setup()/teardown():
# this suite runs under this repo's real commit-time gate with bats
# parallel jobs enabled (pg-test-runner, backed by GNU parallel), so
# per-test mkdir/rm of these SHARED, fixed /tmp paths races -- one test's
# teardown() removing a directory while another concurrently-running
# test's cmd_list is still reading it (confirmed empirically: `bats --jobs
# 4` on this file intermittently failed cmd_list assertions that pass
# every time under `--jobs 1`). None of the registry fixtures' dryRunCommand/
# removeCommand actually touch these directories (they're `echo` stubs), so
# creating them once for the whole file and leaving them in place for every
# test to read is safe and race-free.
setup_file() {
  mkdir -p /tmp/cache-a /tmp/cache-b /tmp/info-only /tmp/failing-item /tmp/slow-item /tmp/ok-item /tmp/low-item /tmp/high-item
}

teardown_file() {
  rm -rf /tmp/cache-a /tmp/cache-b /tmp/info-only /tmp/failing-item /tmp/slow-item /tmp/ok-item /tmp/low-item /tmp/high-item
}

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"

  FIXTURES_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/fixtures" && pwd)"

  # Standard test isolation (bash-scripting skill): isolate HOME, and start
  # with XDG_CONFIG_HOME unset so default-path tests aren't at the mercy of
  # whatever the outer environment happens to have set.
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export REAL_HOME="${HOME:-}"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  unset XDG_CONFIG_HOME
}

teardown() {
  rm -rf "$TEST_DIR"
}

# install_reclaim_registry: copies tests/fixtures/reclaim.json to the
# default (XDG) registry path, matching the "defaults to the XDG
# registry path when none is given" pattern already used above for
# pgdr_read_registry -- cmd_reclaim has no registry-path positional of
# its own, so its tests always exercise the default-path lookup.
install_reclaim_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/reclaim.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
}

# install_list_registry: copies tests/fixtures/list.json to the default
# (XDG) registry path, matching install_reclaim_registry above --
# cmd_list has no registry-path positional of its own either, so its
# tests always exercise the default-path lookup.
install_list_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/list.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
}

# install_list_resilience_registry: copies tests/fixtures/list-resilience.json
# (failing-item, then slow-item, then ok-item -- in that registry order) to
# the default XDG registry path, for exercising pgdr_display_output's
# failure/timeout handling from cmd_list.
install_list_resilience_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/list-resilience.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
}

@test "cmd_validate fails loudly when the registry file is missing" {
  run cmd_validate "$TEST_DIR/does-not-exist.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "registry file not found" ]]
}

@test "cmd_validate defaults to the XDG registry path when none is given" {
  # Uses command-exists.json, not valid.json: valid.json's commands
  # (npm/brew) don't resolve in the nix sandbox's bats run, which only puts
  # jq on PATH (see default.nix's testDeps) -- cmd_validate's 4th check
  # (pgdr_validate_commands_exist) would correctly reject it there.
  # command-exists.json's commands (a builtin, a pg-disk-reclaimer.bash
  # function) resolve anywhere this suite runs.
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/command-exists.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  run cmd_validate
  [ "$status" -eq 0 ]
  [[ "$output" =~ "is valid" ]]
}

@test "cmd_validate propagates a schema check (1-3) failure from pgdr_validate_registry" {
  run cmd_validate "$FIXTURES_DIR/missing-field.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "missing a non-empty id/description/path/displayCommand" ]]
}

@test "cmd_validate accepts a registry whose commands resolve to a real command or a pg-disk-reclaimer.bash helper function" {
  run cmd_validate "$FIXTURES_DIR/command-exists.json"
  [ "$status" -eq 0 ]
  [[ "$output" =~ "is valid" ]]
}

@test "cmd_validate rejects a registry whose command references a nonexistent binary, naming the item and the command" {
  run cmd_validate "$FIXTURES_DIR/command-missing.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "missing-binary-item" ]]
  [[ "$output" =~ "pg-disk-reclaimer-test-nonexistent-cmd-xyz" ]]
}

@test "pgdr_command_exists accepts a real command" {
  run pgdr_command_exists true
  [ "$status" -eq 0 ]
}

@test "pgdr_command_exists accepts a pg-disk-reclaimer.bash-defined function" {
  run pgdr_command_exists pgdr_default_registry_path
  [ "$status" -eq 0 ]
}

@test "pgdr_command_exists rejects a nonexistent command/function" {
  run pgdr_command_exists pg-disk-reclaimer-test-nonexistent-cmd-xyz
  [ "$status" -ne 0 ]
}

@test "cmd_reclaim requires --aggressiveness" {
  run cmd_reclaim
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness" ]]
}

@test "cmd_reclaim requires --aggressiveness even when --apply is given" {
  run cmd_reclaim --apply
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness" ]]
}

@test "pgdr_default_registry_path honors XDG_CONFIG_HOME" {
  export XDG_CONFIG_HOME="$TEST_DIR/xdg-config"
  run pgdr_default_registry_path
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/xdg-config/pg-disk-reclaimer/registry.json" ]
}

@test "pgdr_default_registry_path falls back to \$HOME/.config" {
  run pgdr_default_registry_path
  [ "$status" -eq 0 ]
  [ "$output" = "$HOME/.config/pg-disk-reclaimer/registry.json" ]
}

@test "pgdr_validate_registry accepts a fully valid registry" {
  run pgdr_validate_registry "$FIXTURES_DIR/valid.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgdr_validate_registry accepts an item with an empty variants array" {
  run pgdr_validate_registry "$FIXTURES_DIR/empty-variants.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgdr_validate_registry rejects malformed JSON" {
  # Malformed JSON is generated into TEST_DIR at test time rather than kept
  # as a committed tests/fixtures/*.json file: this repo's treefmt/prettier
  # pre-commit hook parses and reformats every committed *.json file, and it
  # silently REPAIRS a trailing-comma-style syntax error (dropping the comma)
  # instead of erroring -- so a committed "malformed" fixture would stop
  # being malformed the moment it was formatted.
  local malformed="$TEST_DIR/malformed.json"
  cat >"$malformed" <<'JSON'
[
  {
    "id": "broken",
    "description": "broken json",
    "path": "/tmp/broken",
    "displayCommand": "echo broken",
    "variants": [],
  }
]
JSON
  run pgdr_validate_registry "$malformed"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not valid JSON" ]]
}

@test "pgdr_validate_registry rejects an item missing a required field" {
  run pgdr_validate_registry "$FIXTURES_DIR/missing-field.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "missing a non-empty id/description/path/displayCommand" ]]
}

@test "pgdr_validate_registry rejects duplicate ids across items" {
  run pgdr_validate_registry "$FIXTURES_DIR/duplicate-id.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "duplicate id" ]]
}

@test "pgdr_validate_registry rejects duplicate aggressiveness within one item's variants" {
  run pgdr_validate_registry "$FIXTURES_DIR/duplicate-aggressiveness.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "duplicate variant aggressiveness" ]]
}

@test "pgdr_read_registry fails loudly when the registry file is missing" {
  run pgdr_read_registry "$TEST_DIR/does-not-exist.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "registry file not found" ]]
}

@test "pgdr_read_registry fails loudly on a malformed registry" {
  local malformed="$TEST_DIR/malformed.json"
  cat >"$malformed" <<'JSON'
[
  {
    "id": "broken",
    "description": "broken json",
    "path": "/tmp/broken",
    "displayCommand": "echo broken",
    "variants": [],
  }
]
JSON
  run pgdr_read_registry "$malformed"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not valid JSON" ]]
}

@test "pgdr_read_registry prints the parsed registry on success" {
  run pgdr_read_registry "$FIXTURES_DIR/valid.json"
  [ "$status" -eq 0 ]
  [[ "$output" =~ "npm-cache" ]]
}

@test "pgdr_read_registry defaults to the XDG registry path when none is given" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/valid.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  run pgdr_read_registry
  [ "$status" -eq 0 ]
  [[ "$output" =~ "npm-cache" ]]
}

@test "pgdr_select_variants with no ids and N below every variant returns an empty array" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 0
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -c '.')" = "[]" ]
}

@test "pgdr_select_variants with no ids at N=2 selects the single-variant item at its own aggressiveness, the multi-variant item at its lowest qualifying aggressiveness, and never the info-only item" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 2
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq '[.[] | select(.id == "single-variant-item")][0].aggressiveness')" = "2" ]
  [ "$(echo "$output" | jq '[.[] | select(.id == "multi-variant-item")][0].aggressiveness')" = "1" ]
  [ "$(echo "$output" | jq '[.[] | select(.id == "info-only-item")] | length')" = "0" ]
}

@test "pgdr_select_variants with no ids picks the highest qualifying variant strictly between two levels (N=4 picks aggressiveness 3, not 1 or 5)" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 4
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq '[.[] | select(.id == "multi-variant-item")][0].aggressiveness')" = "3" ]
}

@test "pgdr_select_variants with no ids and N at the highest variant level selects that variant" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 5
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq '[.[] | select(.id == "multi-variant-item")][0].aggressiveness')" = "5" ]
}

@test "pgdr_select_variants with an explicit id picks the highest qualifying variant, matching the no-ids rule" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 4 multi-variant-item
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq 'length')" = "1" ]
  [ "$(echo "$output" | jq '.[0].aggressiveness')" = "3" ]
}

@test "pgdr_select_variants errors when an explicit id's minimum variant aggressiveness exceeds N, naming the actual minimum" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 0 multi-variant-item
  [ "$status" -eq 1 ]
  [[ "$output" =~ "item 'multi-variant-item' requires aggressiveness >= 1, but --aggressiveness 0 was given" ]]
}

@test "pgdr_select_variants errors on an explicit id naming the informational-only (zero-variant) item" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 5 info-only-item
  [ "$status" -eq 1 ]
  [[ "$output" == *"item 'info-only-item' is informational-only (no variants) and cannot be selected"* ]]
}

@test "pgdr_select_variants errors on an explicit id that does not exist in the registry" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 5 does-not-exist
  [ "$status" -eq 1 ]
  [[ "$output" =~ "unknown item id 'does-not-exist'" ]]
}

@test "pgdr_select_variants with multiple explicit ids returns selections in registry order, not command-line order" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 5 single-variant-item multi-variant-item
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '[.[].id] | join(",")')" = "multi-variant-item,single-variant-item" ]
}

@test "pgdr_select_variants fails fast on a mix of one valid and one invalid id, with no partial output for the valid one" {
  run pgdr_select_variants "$FIXTURES_DIR/selection.json" 5 single-variant-item does-not-exist
  [ "$status" -eq 1 ]
  [[ "$output" =~ "unknown item id 'does-not-exist'" ]]
  [[ ! "$output" =~ "single-variant-item" ]]
}

# cmd_list (bead pg2-txxyj.4), exercised against tests/fixtures/list.json:
# cache-a (single variant, aggressiveness 1), cache-b (two variants, at
# aggressiveness 2 and 5), and info-only (zero variants). All three
# displayCommand values are side-effect-free `echo` stubs, so the size
# output never touches the real filesystem.
#
# Output is a per-item "ID: / DESCRIPTION: / AGGRESSIVENESS: / <display
# output>" block followed by a "---" separator, NOT a fixed-width table --
# displayCommand strings have no contract to produce single-line output (see
# cmd_list's own doc comment), so tests here check for the presence of
# specific header/output lines rather than asserting an exact table layout.

@test "cmd_list with no --aggressiveness lists every item, including the zero-variant informational one" {
  install_list_registry
  run cmd_list
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: cache-a" ]]
  [[ "$output" =~ "ID: cache-b" ]]
  [[ "$output" =~ "ID: info-only" ]]
}

@test "cmd_list --aggressiveness 1 excludes cache-b (no variant <= 1) and info-only (no variants), keeps cache-a" {
  install_list_registry
  run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: cache-a" ]]
  [[ ! "$output" =~ "ID: cache-b" ]]
  [[ ! "$output" =~ "ID: info-only" ]]
}

@test "cmd_list --aggressiveness 5 includes a qualifying multi-variant item, showing every aggressiveness value it has (not just the chosen one)" {
  install_list_registry
  run cmd_list --aggressiveness 5
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: cache-a" ]]
  [[ ! "$output" =~ "ID: info-only" ]]
  [[ "$output" =~ "AGGRESSIVENESS: 2,5" ]]
}

@test "cmd_list --aggressiveness below every variant prints nothing" {
  install_list_registry
  run cmd_list --aggressiveness 0
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "cmd_list's displayed output reflects each item's (mocked) displayCommand output" {
  install_list_registry
  run cmd_list
  [ "$status" -eq 0 ]
  [[ "$output" =~ "10M" ]]
  [[ "$output" =~ "250M" ]]
  [[ "$output" == *"1.0G"* ]]
}

@test "cmd_list renders a header block, then the display command's output, then a separator, per item" {
  install_list_registry
  run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "ID: cache-a" ]
  [ "${lines[1]}" = "DESCRIPTION: cache A, single low-aggressiveness variant" ]
  [ "${lines[2]}" = "AGGRESSIVENESS: 1" ]
  [ "${lines[3]}" = "10M" ]
  [ "${lines[4]}" = "---" ]
  [ "${#lines[@]}" -eq 5 ]
}

@test "cmd_list shows a zero-variant informational item with an exact '-' aggressiveness marker (not just any hyphen in its text)" {
  install_list_registry
  run cmd_list
  [ "$status" -eq 0 ]
  [[ "$output" =~ "AGGRESSIVENESS: -" ]]
}

@test "cmd_list rejects --aggressiveness given with no value" {
  install_list_registry
  run cmd_list --aggressiveness
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness requires a value" ]]
}

@test "cmd_list rejects an unknown option" {
  install_list_registry
  run cmd_list --bogus
  [ "$status" -ne 0 ]
  [[ "$output" =~ "unknown option" ]]
}

@test "cmd_list fails loudly when the registry is missing" {
  run cmd_list
  [ "$status" -ne 0 ]
  [[ "$output" =~ "registry file not found" ]]
}

@test "cmd_list fails loudly on a malformed registry, without printing any item output" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cat >"$HOME/.config/pg-disk-reclaimer/registry.json" <<'JSON'
[
  {
    "id": "broken",
    "description": "broken json",
    "path": "/tmp/broken",
    "displayCommand": "echo broken",
    "variants": [],
  }
]
JSON
  run cmd_list
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not valid JSON" ]]
  [[ ! "$output" =~ "ID:" ]]
}

# cmd_list resilience (this bug fix), exercised against
# tests/fixtures/list-resilience.json: failing-item (exits 7) listed BEFORE
# slow-item (outlives the timeout) and ok-item -- proving a failing/slow
# item does not abort the run and later items still get listed. Before this
# fix, cmd_list ran `size=$(eval "$display_command" 2>/dev/null)` as a bare
# assignment; under the nix wrapper's `set -euo pipefail` a non-zero exit
# there aborted the ENTIRE script (reproduced directly: a real `du -sh` over
# a volume with a permission-denied subdirectory killed the run after the
# first item, silently, since stderr was discarded).

@test "pgdr_display_output never triggers errexit in a caller running under 'set -e' (regression: a bare \$(...) assignment previously did, and the real nix-wrapped binary always runs under 'set -euo pipefail')" {
  # bats' own `run` helper neutralizes errexit around the command it
  # captures, so it CANNOT exercise this failure mode against
  # `run pgdr_display_output ...` directly -- that's exactly how the
  # original bug (and the first draft of this fix) slipped past this same
  # test suite. Spawning a genuinely separate `bash -c 'set -e; ...'`
  # subprocess -- the same shape the nix wrapper runs the real script
  # under -- is what actually exercises it.
  run bash -c '
    set -euo pipefail
    source "$1/pg-disk-reclaimer.bash"
    pgdr_display_output "exit 5"
    echo "AFTER"
  ' -- "$SCRIPTS_DIR"
  [ "$status" -eq 0 ]
  [[ "$output" == *"(display command exited 5)"* ]]
  [[ "$output" =~ "AFTER" ]]
}

@test "cmd_list continues to later items after an earlier item's displayCommand fails, and shows the failure inline" {
  install_list_resilience_registry
  run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: failing-item" ]]
  [[ "$output" == *"(display command exited 7)"* ]]
  [[ "$output" =~ "partial-output" ]]
  [[ "$output" =~ "ID: ok-item" ]]
  [[ "$output" =~ "OK-SIZE" ]]
}

@test "cmd_list bounds a slow displayCommand to PGDR_DISPLAY_TIMEOUT_SECONDS and still lists the item after it" {
  install_list_resilience_registry
  PGDR_DISPLAY_TIMEOUT_SECONDS=1 run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: slow-item" ]]
  [[ "$output" == *"(display command timed out after 1s)"* ]]
  [[ ! "$output" =~ "should-not-appear" ]]
  [[ "$output" =~ "ID: ok-item" ]]
  [[ "$output" =~ "OK-SIZE" ]]
}

# cmd_list path-existence guard (this bug fix, operator dogfooding
# feedback): a registry item whose `path` does not currently exist on this
# machine has nothing to reclaim, so it's skipped by default and shown only
# under --verbose -- and even under --verbose, its displayCommand is never
# run (so no `du: cannot access ...`-style noise ever reaches the output).
# Built inline via heredoc into the default XDG registry path, following
# this file's existing "malformed.json" inline-fixture pattern above,
# rather than a new committed tests/fixtures/*.json file.

install_missing_path_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cat >"$HOME/.config/pg-disk-reclaimer/registry.json" <<'JSON'
[
  {
    "id": "missing-path-item",
    "description": "item whose path does not exist on this machine",
    "path": "/tmp/pg-disk-reclaimer-test-missing-xyz",
    "displayCommand": "du -sh /tmp/pg-disk-reclaimer-test-missing-xyz",
    "variants": [
      {
        "aggressiveness": 1,
        "variantDescription": "n/a",
        "dryRunCommand": "echo dry-missing",
        "removeCommand": "echo remove-missing"
      }
    ]
  }
]
JSON
}

@test "cmd_list skips an item whose path does not exist, by default (no --verbose)" {
  install_missing_path_registry
  run cmd_list
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "cmd_list --verbose shows an item whose path does not exist, without running its displayCommand" {
  install_missing_path_registry
  run cmd_list --verbose
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: missing-path-item" ]]
  [[ "$output" =~ "does not exist" ]]
  [[ ! "$output" =~ "du:" ]]
  [[ ! "$output" =~ "cannot access" ]]
  [[ ! "$output" =~ "No such file" ]]
}

@test "cmd_list -v is the same as --verbose for a missing-path item" {
  install_missing_path_registry
  run cmd_list -v
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ID: missing-path-item" ]]
  [[ "$output" =~ "does not exist" ]]
}

@test "cmd_list separates item blocks with a blank line after each '---' separator" {
  install_list_registry
  run cmd_list --aggressiveness 5
  [ "$status" -eq 0 ]
  [[ "$output" == *$'---\n\nID: cache-b'* ]]
}

# cmd_reclaim (bead pg2-txxyj.6), exercised against tests/fixtures/reclaim.json
# (install_reclaim_registry above): low-item (aggressiveness 1),
# high-item (aggressiveness 5, so it trips the >=4 confirm gate under
# --apply), and failing-item (aggressiveness 1, both commands exit 3, for
# exit-code propagation). pgdr_confirm is always overridden below rather
# than exercised for real, per its own doc comment: the real
# implementation reads /dev/tty and must never be hit by a test.

@test "cmd_reclaim without --apply runs dryRunCommand, not removeCommand" {
  install_reclaim_registry
  run cmd_reclaim --aggressiveness 1 low-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "dry-run-low" ]]
  [[ ! "$output" =~ "remove-low" ]]
}

@test "cmd_reclaim --apply runs removeCommand, not dryRunCommand, for a qualifying item" {
  install_reclaim_registry
  run cmd_reclaim --aggressiveness 1 --apply low-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "remove-low" ]]
  [[ ! "$output" =~ "dry-run-low" ]]
}

@test "cmd_reclaim's confirm gate fires under --apply when aggressiveness >= 4, and a 'yes' proceeds to removeCommand" {
  install_reclaim_registry
  pgdr_confirm() {
    echo "CONFIRM-CALLED:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 5 --apply high-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "CONFIRM-CALLED" ]]
  [[ "$output" =~ "remove-high" ]]
}

@test "cmd_reclaim's confirm gate never fires on a dry run, even at aggressiveness >= 4" {
  install_reclaim_registry
  pgdr_confirm() {
    echo "CONFIRM-CALLED:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 5 high-item
  [ "$status" -eq 0 ]
  [[ ! "$output" =~ "CONFIRM-CALLED" ]]
  [[ "$output" =~ "dry-run-high" ]]
}

@test "cmd_reclaim's confirm gate never fires under --apply when aggressiveness < 4" {
  install_reclaim_registry
  pgdr_confirm() {
    echo "CONFIRM-CALLED:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 1 --apply low-item
  [ "$status" -eq 0 ]
  [[ ! "$output" =~ "CONFIRM-CALLED" ]]
  [[ "$output" =~ "remove-low" ]]
}

@test "cmd_reclaim's confirm gate: a 'no' answer skips removeCommand for that item only, other selected items still proceed" {
  install_reclaim_registry
  pgdr_confirm() { return 1; }
  run cmd_reclaim --aggressiveness 5 --apply low-item high-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "remove-low" ]]
  [[ "$output" =~ "skipping 'high-item'" ]]
  [[ ! "$output" =~ "remove-high" ]]
}

@test "cmd_reclaim requires --aggressiveness even with an explicit id given" {
  install_reclaim_registry
  run cmd_reclaim low-item
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness" ]]
}

@test "cmd_reclaim propagates a failing dryRunCommand's exit as overall failure" {
  install_reclaim_registry
  run cmd_reclaim --aggressiveness 1 failing-item
  [ "$status" -ne 0 ]
  [[ "$output" =~ "dry-run-fail" ]]
}

@test "cmd_reclaim propagates a failing removeCommand's exit as overall failure" {
  install_reclaim_registry
  run cmd_reclaim --aggressiveness 1 --apply failing-item
  [ "$status" -ne 0 ]
  [[ "$output" =~ "remove-fail" ]]
}

@test "cmd_reclaim continues to other selected items after one item's removeCommand fails" {
  install_reclaim_registry
  run cmd_reclaim --aggressiveness 1 --apply low-item failing-item
  [ "$status" -ne 0 ]
  [[ "$output" =~ "remove-low" ]]
  [[ "$output" =~ "remove-fail" ]]
}

@test "cmd_reclaim surfaces pgdr_select_variants' id errors as-is (no id re-validation of its own)" {
  install_reclaim_registry
  run cmd_reclaim --aggressiveness 5 does-not-exist
  [ "$status" -ne 0 ]
  [[ "$output" =~ "unknown item id 'does-not-exist'" ]]
}
