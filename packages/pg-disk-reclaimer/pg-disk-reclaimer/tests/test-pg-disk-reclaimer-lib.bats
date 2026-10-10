#!/usr/bin/env bats
# Unit tests for pg-disk-reclaimer's core subcommand functions
# (pg-disk-reclaimer.bash). list, validate, and reclaim are all now
# implemented (beads pg2-txxyj.4/.5/.6) -- only the scaffold task
# (pg2-txxyj.1) ever left them as stubs.
# `run --separate-stderr` (bats >= 1.5.0, precedent: pg-wi-flow's lib tests)
# is used below wherever a case mixes a stderr error message with stdout
# JSON/text, so the error line is never mistaken for part of stdout.
bats_require_minimum_version 1.5.0
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
# real-item (under SHARED_TMPDIR below) is this bead's own addition
# (cmd_reclaim's path-existence guard, pg2-eqniv.2): the "real, existing
# path" half of the mixed missing/real registry used by the guard tests
# below.
#
# Deliberately setup_file/teardown_file, NOT per-test setup()/teardown():
# this suite runs under this repo's real commit-time gate with bats
# parallel jobs enabled (pg-test-runner, backed by GNU parallel), so
# per-test mkdir/rm of these SHARED paths races -- one test's teardown()
# removing a directory while another concurrently-running test's cmd_list
# is still reading it (confirmed empirically: `bats --jobs 4` on this file
# intermittently failed cmd_list assertions that pass every time under
# `--jobs 1`). None of the registry fixtures' dryRunCommand/removeCommand
# actually touch these directories (they're `echo` stubs), so creating
# them once for the whole file and leaving them in place for every test to
# read is safe and race-free.
#
# SHARED_TMPDIR (bug fix pg2-aun3f): a per-run `mktemp -d` directory,
# matching test-pg-disk-reclaimer-completions.bats's TEST_DIR pattern --
# NOT the literal /tmp/cache-a et al. paths this file used to mkdir/rm
# directly. Those fixed names collided across concurrent nix-sandboxed
# runs of this same suite: one run's leftover fixture dirs, owned by a
# different nix build user, blocked a later run's own teardown_file with
# `rm -rf: Permission denied` (see the bead for the repro). setup_file
# exports SHARED_TMPDIR; each @test's process forks from the same parent
# that ran setup_file (and teardown_file runs in that same parent
# process), so the exported value is visible everywhere it's needed --
# verified empirically against this bats version. The committed fixture
# JSON files under tests/fixtures/ still hardcode their own /tmp/* `path`
# values (left untouched, so they stay valid standalone fixtures);
# install_list_registry / install_reclaim_registry /
# install_list_resilience_registry below rewrite those literal paths to
# their SHARED_TMPDIR equivalent via retarget_fixture_paths (jq) instead
# of a plain `cp`, so cmd_list/cmd_reclaim's path-existence guard checks a
# real, run-isolated directory rather than a fixed /tmp name.
setup_file() {
  SHARED_TMPDIR="$(mktemp -d)"
  export SHARED_TMPDIR
  mkdir -p "$SHARED_TMPDIR"/{cache-a,cache-b,info-only,failing-item,slow-item,long-item,ok-item,low-item,high-item,real-item}
}

teardown_file() {
  rm -rf "$SHARED_TMPDIR"
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

# retarget_fixture_paths <fixture-file> <dest-file> <old-path>=<new-path>...:
# copies <fixture-file> to <dest-file>, rewriting each item's `path` field
# from a fixture's committed literal /tmp/* value to this run's
# SHARED_TMPDIR-based equivalent (see setup_file's doc comment above) --
# used instead of a plain `cp` so cmd_list/cmd_reclaim's path-existence
# guard sees a real, per-run-isolated directory rather than a fixed name
# shared across concurrent nix-sandboxed runs of this suite.
retarget_fixture_paths() {
  local fixture="$1" dest="$2"
  shift 2
  local filter='.'
  local -a jq_args=()
  local i=0
  local pair old new
  for pair in "$@"; do
    old="${pair%%=*}"
    new="${pair#*=}"
    jq_args+=(--arg "old$i" "$old" --arg "new$i" "$new")
    filter="$filter | map(.path |= (if . == \$old$i then \$new$i else . end))"
    i=$((i + 1))
  done
  jq "${jq_args[@]}" "$filter" "$fixture" >"$dest"
}

# install_reclaim_registry: installs tests/fixtures/reclaim.json (path
# fields retargeted to SHARED_TMPDIR, see retarget_fixture_paths above) to
# the default (XDG) registry path, matching the "defaults to the XDG
# registry path when none is given" pattern already used above for
# pgdr_read_registry -- cmd_reclaim has no registry-path positional of
# its own, so its tests always exercise the default-path lookup.
install_reclaim_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  retarget_fixture_paths "$FIXTURES_DIR/reclaim.json" \
    "$HOME/.config/pg-disk-reclaimer/registry.json" \
    "/tmp/low-item=$SHARED_TMPDIR/low-item" \
    "/tmp/high-item=$SHARED_TMPDIR/high-item" \
    "/tmp/failing-item=$SHARED_TMPDIR/failing-item"
}

# install_list_registry: installs tests/fixtures/list.json (path fields
# retargeted to SHARED_TMPDIR) to the default (XDG) registry path,
# matching install_reclaim_registry above -- cmd_list has no
# registry-path positional of its own either, so its tests always
# exercise the default-path lookup.
install_list_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  retarget_fixture_paths "$FIXTURES_DIR/list.json" \
    "$HOME/.config/pg-disk-reclaimer/registry.json" \
    "/tmp/cache-a=$SHARED_TMPDIR/cache-a" \
    "/tmp/cache-b=$SHARED_TMPDIR/cache-b" \
    "/tmp/info-only=$SHARED_TMPDIR/info-only"
}

# install_list_resilience_registry: installs
# tests/fixtures/list-resilience.json (failing-item, then slow-item, then
# long-item (has its own displayTimeoutSeconds), then ok-item -- in that
# registry order; path fields retargeted to
# SHARED_TMPDIR) to the default XDG registry path, for exercising
# pgdr_display_output's failure/timeout handling from cmd_list.
install_list_resilience_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  retarget_fixture_paths "$FIXTURES_DIR/list-resilience.json" \
    "$HOME/.config/pg-disk-reclaimer/registry.json" \
    "/tmp/failing-item=$SHARED_TMPDIR/failing-item" \
    "/tmp/slow-item=$SHARED_TMPDIR/slow-item" \
    "/tmp/long-item=$SHARED_TMPDIR/long-item" \
    "/tmp/ok-item=$SHARED_TMPDIR/ok-item"
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

@test "pgdr_select_variants on a mix of one valid and one invalid id still selects the valid one, reporting the bad one and returning 1 (bug fix, bead pg2-qt7ep: previously failed fast with no partial output at all)" {
  # --separate-stderr (bats >= 1.5.0, precedent: pg-wi-flow's lib tests):
  # this case's stdout is a pure JSON selection AND stderr carries the bad-id
  # error, both non-empty -- $output alone (stdout+stderr combined) is not
  # valid JSON, so it must be parsed separately from $stderr.
  run --separate-stderr pgdr_select_variants "$FIXTURES_DIR/selection.json" 5 single-variant-item does-not-exist
  [ "$status" -eq 1 ]
  [[ "$stderr" =~ "unknown item id 'does-not-exist'" ]]
  [ "$(echo "$output" | jq -c '[.[].id]')" = '["single-variant-item"]' ]
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

# macOS TCC denial (bead pg2-qqyq5): `du -sh ~/.Trash` without Full Disk Access
# prints "du: cannot read directory ...: Operation not permitted" PLUS a
# misleading "0" total, and exits 1. That 0 must never be shown as a size.
@test "pgdr_display_output reports a TCC denial as size unavailable (no Full Disk Access), not the misleading partial 0" {
  run pgdr_display_output "echo \"du: cannot read directory '/x/.Trash': Operation not permitted\" >&2; printf '0\\t/x/.Trash\\n'; exit 1"
  [ "$status" -eq 0 ]
  [[ "$output" == *"size unavailable (no Full Disk Access)"* ]]
  [[ "$output" != *"(display command exited"* ]]
  [[ "$output" != *$'0\t/x/.Trash'* ]]
}

@test "pgdr_display_output still shows a non-TCC failure with its exit code and partial output" {
  run pgdr_display_output "echo 'du: x: Permission denied' >&2; echo 5M; exit 1"
  [ "$status" -eq 0 ]
  [[ "$output" == *"(display command exited 1)"* ]]
  [[ "$output" == *"5M"* ]]
  [[ "$output" != *"no Full Disk Access"* ]]
}

@test "pgdr_display_output does not treat 'Operation not permitted' text in a SUCCESSFUL command's output as a TCC denial" {
  run pgdr_display_output "echo 'Operation not permitted is just text here'"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Operation not permitted is just text here"* ]]
  [[ "$output" != *"no Full Disk Access"* ]]
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

# Per-item displayTimeoutSeconds override + concurrent display (bead
# pg2-m6bsb), against the same list-resilience fixture: long-item sleeps 2s
# and declares displayTimeoutSeconds 10, slow-item sleeps 5s with no
# override, so under a 1s global ceiling only slow-item times out.

@test "cmd_list lets an item's displayTimeoutSeconds exceed the global ceiling, while items without it keep the global ceiling" {
  install_list_resilience_registry
  PGDR_DISPLAY_TIMEOUT_SECONDS=1 run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" =~ "LONG-SIZE" ]]
  [[ "$output" == *"(display command timed out after 1s)"* ]]
  [[ ! "$output" =~ "should-not-appear" ]]
}

@test "cmd_list reports a per-item timeout's own value when that item times out" {
  install_list_resilience_registry
  jq 'map(if .id == "long-item" then .displayTimeoutSeconds = 1 else . end)' \
    "$HOME/.config/pg-disk-reclaimer/registry.json" >"$TEST_DIR/r.json"
  mv "$TEST_DIR/r.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  PGDR_DISPLAY_TIMEOUT_SECONDS=30 run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ ! "$output" =~ "LONG-SIZE" ]]
  [[ "$output" == *"(display command timed out after 1s)"* ]]
}

@test "cmd_list prints items in registry order even though displayCommands run concurrently" {
  install_list_resilience_registry
  PGDR_DISPLAY_TIMEOUT_SECONDS=1 run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  local failing slow long ok
  failing=${output%%ID: failing-item*}
  slow=${output%%ID: slow-item*}
  long=${output%%ID: long-item*}
  ok=${output%%ID: ok-item*}
  [ "${#failing}" -lt "${#slow}" ]
  [ "${#slow}" -lt "${#long}" ]
  [ "${#long}" -lt "${#ok}" ]
}

@test "cmd_list runs displayCommands concurrently: each item waits for all its siblings to have started (load-independent, no wall-clock threshold)" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer" "$SHARED_TMPDIR/conc" "$TEST_DIR/barrier"
  # Each item drops a marker, then polls until all 4 markers exist. Run
  # serially the first item can never see the others and prints nothing
  # (and would time out); run concurrently every item sees all 4.
  jq -n --arg p "$SHARED_TMPDIR/conc" --arg b "$TEST_DIR/barrier" '
    [range(0; 4) | {
      id: "conc-\(.)", description: "waits for siblings", path: $p,
      displayCommand: ("touch \($b)/m\(.); for i in $(seq 200); do [ $(ls \($b) | wc -l) -ge 4 ] && echo CONC-DONE && exit 0; sleep 0.1; done; echo CONC-SERIAL"),
      variants: [{aggressiveness: 1, variantDescription: "n/a",
                  dryRunCommand: "echo d", removeCommand: "echo r"}]
    }]' >"$HOME/.config/pg-disk-reclaimer/registry.json"
  PGDR_DISPLAY_TIMEOUT_SECONDS=60 run cmd_list --aggressiveness 1
  [ "$status" -eq 0 ]
  [ "$(grep -c CONC-DONE <<<"$output")" -eq 4 ]
  [[ ! "$output" =~ "CONC-SERIAL" ]]
}

# validate: displayTimeoutSeconds, when present, must be a positive integer.

@test "pgdr_validate_registry accepts a positive-integer displayTimeoutSeconds" {
  install_list_resilience_registry
  run --separate-stderr pgdr_validate_registry "$HOME/.config/pg-disk-reclaimer/registry.json"
  [ "$status" -eq 0 ]
}

@test "pgdr_validate_registry rejects a non-positive, non-integer, or non-numeric displayTimeoutSeconds" {
  install_list_resilience_registry
  local reg="$HOME/.config/pg-disk-reclaimer/registry.json" bad
  for bad in 0 -5 1.5 '"10"' null true; do
    jq --argjson v "$bad" 'map(if .id == "long-item" then .displayTimeoutSeconds = $v else . end)' \
      "$reg" >"$TEST_DIR/bad.json"
    run --separate-stderr pgdr_validate_registry "$TEST_DIR/bad.json"
    [ "$status" -ne 0 ]
    [[ "$stderr" == *"item at index 2 has an invalid displayTimeoutSeconds"* ]]
  done
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

# Regression test, bead pg2-qt7ep: a real reclaim run was observed to stop
# partway through instead of attempting every registry item. Reproduced
# directly against the pre-fix code: `cmd_reclaim --aggressiveness N id1
# bad-id id2` never ran id1 or id2's commands at all, because
# pgdr_select_variants' Case B (explicit ids) failed fast on the FIRST bad
# id and returned before producing any selection -- registry validation
# (pgdr_read_registry) was confirmed NOT to be the culprit (it passes fine
# on a well-formed registry regardless of which explicit ids are given).
# This test mixes one unknown id with THREE good ids -- including
# failing-item, whose own command also exits non-zero -- so it proves both
# halves of the fix at once: a bad id must not block ANY good id (this is
# the regression), and a good id's own failing command must still not
# block any OTHER good id (pre-existing per-item-loop behavior, still
# exercised end-to-end here). The final exit status must reflect the
# mixed outcome (bad id + one failing command) without ever having
# stopped short.
@test "cmd_reclaim attempts every valid id when one explicit id among several is bad, and the mixed outcome is still reflected in the exit status" {
  install_reclaim_registry
  run --separate-stderr cmd_reclaim --aggressiveness 5 low-item does-not-exist high-item failing-item
  [ "$status" -ne 0 ]
  [[ "$stderr" =~ "unknown item id 'does-not-exist'" ]]
  [[ "$output" =~ "dry-run-low" ]]
  [[ "$output" =~ "dry-run-high" ]]
  [[ "$output" =~ "dry-run-fail" ]]
}

# cmd_reclaim path-existence guard (this bead, pg2-eqniv.2, mirroring
# cmd_list's guard above from pg2-eqniv.1): a selected item whose `path`
# does not currently exist on this machine has nothing to reclaim, so
# neither dryRunCommand nor removeCommand is ever run for it -- silently
# by default (no output, and NOT counted against overall_status, same as
# a declined confirm-gate), or with one stderr note under --verbose.
# Built inline via heredoc into the default XDG registry path, following
# this file's existing "malformed.json"/install_missing_path_registry
# inline-fixture pattern above, rather than a new committed
# tests/fixtures/*.json file. Both variants' commands are the literal
# `echo SHOULD-NOT-RUN` sentinel from the bead spec, so any test failure
# to actually skip the item is immediately visible in the output.

install_reclaim_missing_path_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cat >"$HOME/.config/pg-disk-reclaimer/registry.json" <<'JSON'
[
  {
    "id": "missing-path-item",
    "description": "item whose path does not exist on this machine",
    "path": "/tmp/pg-disk-reclaimer-test-missing-xyz",
    "displayCommand": "echo missing-path-item",
    "variants": [
      {
        "aggressiveness": 1,
        "variantDescription": "n/a",
        "dryRunCommand": "echo SHOULD-NOT-RUN",
        "removeCommand": "echo SHOULD-NOT-RUN"
      }
    ]
  }
]
JSON
}

# install_reclaim_mixed_path_registry: one missing-path item plus one item
# whose path ($SHARED_TMPDIR/real-item) genuinely exists (mkdir'd/rm'd in
# setup_file/teardown_file above, alongside this file's other shared
# fixture paths -- see that comment for why NOT per-test setup()/
# teardown()) -- for confirming the guard applies per-item, not to the
# whole run. Unlike malformed.json's inline fixture below, this heredoc
# is UNQUOTED (<<JSON, not <<'JSON') so $SHARED_TMPDIR expands.
install_reclaim_mixed_path_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cat >"$HOME/.config/pg-disk-reclaimer/registry.json" <<JSON
[
  {
    "id": "missing-path-item",
    "description": "item whose path does not exist on this machine",
    "path": "/tmp/pg-disk-reclaimer-test-missing-xyz",
    "displayCommand": "echo missing-path-item",
    "variants": [
      {
        "aggressiveness": 1,
        "variantDescription": "n/a",
        "dryRunCommand": "echo SHOULD-NOT-RUN",
        "removeCommand": "echo SHOULD-NOT-RUN"
      }
    ]
  },
  {
    "id": "real-item",
    "description": "item whose path exists on this machine",
    "path": "$SHARED_TMPDIR/real-item",
    "displayCommand": "echo real-item",
    "variants": [
      {
        "aggressiveness": 1,
        "variantDescription": "n/a",
        "dryRunCommand": "echo dry-real",
        "removeCommand": "echo remove-real"
      }
    ]
  }
]
JSON
}

@test "cmd_reclaim skips a missing-path item by default (dry run, no --verbose): no SHOULD-NOT-RUN, no output at all" {
  install_reclaim_missing_path_registry
  run cmd_reclaim --aggressiveness 1 missing-path-item
  [ "$status" -eq 0 ]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
  [ -z "$output" ]
}

@test "cmd_reclaim --verbose shows the skip note for a missing-path item (dry run), still no SHOULD-NOT-RUN" {
  install_reclaim_missing_path_registry
  run cmd_reclaim --aggressiveness 1 --verbose missing-path-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "does not exist -- nothing to do, skipping" ]]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
}

@test "cmd_reclaim --apply skips a missing-path item by default (no --verbose): no SHOULD-NOT-RUN" {
  install_reclaim_missing_path_registry
  run cmd_reclaim --aggressiveness 1 --apply missing-path-item
  [ "$status" -eq 0 ]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
}

@test "cmd_reclaim --apply --verbose shows the skip note for a missing-path item, still no SHOULD-NOT-RUN" {
  install_reclaim_missing_path_registry
  run cmd_reclaim --aggressiveness 1 --apply --verbose missing-path-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "does not exist -- nothing to do, skipping" ]]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
}

@test "cmd_reclaim -v is the same as --verbose for a missing-path item under --apply" {
  install_reclaim_missing_path_registry
  run cmd_reclaim --aggressiveness 1 --apply -v missing-path-item
  [ "$status" -eq 0 ]
  [[ "$output" =~ "does not exist -- nothing to do, skipping" ]]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
}

@test "cmd_reclaim mixed case: the real item's dry-run command still runs and the missing-path item is silently skipped (no --verbose)" {
  install_reclaim_mixed_path_registry
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" =~ "dry-real" ]]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
  [[ ! "$output" =~ "does not exist" ]]
}

@test "cmd_reclaim mixed case under --verbose: the real item's dry-run command still runs and the missing-path item shows its skip note" {
  install_reclaim_mixed_path_registry
  run cmd_reclaim --aggressiveness 1 --verbose
  [ "$status" -eq 0 ]
  [[ "$output" =~ "dry-real" ]]
  [[ "$output" =~ "does not exist -- nothing to do, skipping" ]]
  [[ ! "$output" =~ "SHOULD-NOT-RUN" ]]
}

# cmd_reclaim reclaimable-size reporting (bead pg2-es6fn).
#
# Every item that cmd_reclaim actually runs (dry run or --apply) gets a
# `<id>: size: <size>` line -- or an explicit `size: unknown (<reason>)`
# marker, never silence -- and the run ends with a total that sums ONLY the
# known sizes and says how many items were unknown. Skipped items (missing
# path, declined confirm gate) get no size line and are not counted.
#
# Registries are built inline with jq -n under this test's own TEST_DIR
# (per-test isolation), not committed as fixtures: several cases need the
# per-item paths and sizeCommand strings to vary.

# install_size_registry <item-json>...: writes a registry from the given
# item objects to the default XDG registry path. Build each item with
# size_item below.
install_size_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  jq -s '.' >"$HOME/.config/pg-disk-reclaimer/registry.json" < <(printf '%s\n' "$@")
}

# size_item <id> <path> <sizeCommand-or-empty> <dryRunCommand> [aggressiveness]:
# prints one registry item as JSON. An empty sizeCommand omits the field
# (so the du-over-path default applies).
size_item() {
  jq -n --arg id "$1" --arg path "$2" --arg sc "$3" --arg dry "$4" \
    --argjson aggr "${5:-1}" '
    {
      id: $id,
      description: ("size test item " + $id),
      path: $path,
      displayCommand: "true",
      variants: [{
        aggressiveness: $aggr,
        variantDescription: "size test",
        dryRunCommand: $dry,
        removeCommand: ("echo remove-" + $id)
      }]
    } + (if $sc == "" then {} else {sizeCommand: $sc} end)'
}

@test "pgdr_format_kb renders KiB as a short human size" {
  run pgdr_format_kb 0
  [ "$output" = "0K" ]
  run pgdr_format_kb 1023
  [ "$output" = "1023K" ]
  run pgdr_format_kb 1536
  [ "$output" = "1.5M" ]
  run pgdr_format_kb 68500000
  [ "$output" = "65.3G" ]
  run pgdr_format_kb 2147483648
  [ "$output" = "2.0T" ]
}

@test "cmd_reclaim dry run prints a known size per item and a total" {
  mkdir -p "$TEST_DIR/known"
  install_size_registry "$(size_item known "$TEST_DIR/known" 'echo 2048' 'echo dry-known')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"known: size: 2.0M"* ]]
  [[ "$output" == *"dry-known"* ]]
  [[ "$output" == *"total reclaimable: 2.0M (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim sizes an item with du over its path when it has no sizeCommand" {
  mkdir -p "$TEST_DIR/duitem"
  dd if=/dev/zero of="$TEST_DIR/duitem/blob" bs=1024 count=1024 2>/dev/null
  install_size_registry "$(size_item duitem "$TEST_DIR/duitem" '' 'echo dry-du')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" =~ duitem:\ size:\ [0-9.]+[KMGT] ]]
  [[ "$output" == *"(1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim prints an explicit unknown marker when the size times out, still runs the dry run, and counts it unknown" {
  mkdir -p "$TEST_DIR/slow"
  install_size_registry "$(size_item slow "$TEST_DIR/slow" 'sleep 5' 'echo dry-slow')"
  PGDR_SIZE_TIMEOUT_SECONDS=1 run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"slow: size: unknown (timed out after 1s)"* ]]
  [[ "$output" == *"dry-slow"* ]]
  [[ "$output" == *"total reclaimable: 0K (0 sized, 1 unknown)"* ]]
}

@test "cmd_reclaim sizing honours an item's displayTimeoutSeconds above the global size ceiling" {
  mkdir -p "$TEST_DIR/patient" "$HOME/.config/pg-disk-reclaimer"
  size_item patient "$TEST_DIR/patient" 'sleep 2; echo 512' 'echo dry-patient' |
    jq '. + {displayTimeoutSeconds: 10}' | jq -s '.' >"$HOME/.config/pg-disk-reclaimer/registry.json"
  PGDR_SIZE_TIMEOUT_SECONDS=1 run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"patient: size: 512K"* ]]
  [[ "$output" == *"total reclaimable: 512K (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim sizing still times out when the item's displayTimeoutSeconds is exceeded" {
  mkdir -p "$TEST_DIR/stuck" "$HOME/.config/pg-disk-reclaimer"
  size_item stuck "$TEST_DIR/stuck" 'sleep 5; echo 512' 'echo dry-stuck' |
    jq '. + {displayTimeoutSeconds: 1}' | jq -s '.' >"$HOME/.config/pg-disk-reclaimer/registry.json"
  PGDR_SIZE_TIMEOUT_SECONDS=1 run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"stuck: size: unknown (timed out after 1s)"* ]]
}

@test "pgdr_item_size_timeout is the larger of the global ceiling and the item's displayTimeoutSeconds" {
  PGDR_SIZE_TIMEOUT_SECONDS=60 run pgdr_item_size_timeout '{"id":"a"}'
  [ "$output" = "60" ]
  PGDR_SIZE_TIMEOUT_SECONDS=60 run pgdr_item_size_timeout '{"id":"a","displayTimeoutSeconds":300}'
  [ "$output" = "300" ]
  PGDR_SIZE_TIMEOUT_SECONDS=60 run pgdr_item_size_timeout '{"id":"a","displayTimeoutSeconds":30}'
  [ "$output" = "60" ]
  PGDR_SIZE_TIMEOUT_SECONDS=60 run pgdr_item_size_timeout ''
  [ "$output" = "60" ]
}

@test "pgdr_select_variants carries an item's displayTimeoutSeconds and sizeCommand into the selection, and omits them when absent" {
  local reg="$TEST_DIR/sel-timeout.json"
  {
    size_item with "$TEST_DIR/with" 'echo 1' 'true' | jq '. + {displayTimeoutSeconds: 120}'
    size_item without "$TEST_DIR/without" '' 'true'
  } | jq -s '.' >"$reg"
  run pgdr_select_variants "$reg" 1
  [ "$status" -eq 0 ]
  [ "$(jq -r '.[] | select(.id == "with") | .displayTimeoutSeconds' <<<"$output")" = "120" ]
  [ "$(jq -r '.[] | select(.id == "with") | .sizeCommand' <<<"$output")" = "echo 1" ]
  [ "$(jq -r '.[] | select(.id == "without") | has("displayTimeoutSeconds") or has("sizeCommand")' <<<"$output")" = "false" ]
  run pgdr_select_variants "$reg" 1 with
  [ "$status" -eq 0 ]
  [ "$(jq -r '.[0].displayTimeoutSeconds' <<<"$output")" = "120" ]
}

@test "cmd_reclaim prints an explicit unknown marker when the size command yields no number" {
  mkdir -p "$TEST_DIR/junk"
  install_size_registry "$(size_item junk "$TEST_DIR/junk" 'echo not-a-number' 'echo dry-junk')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"junk: size: unknown (size command gave no number)"* ]]
  [[ "$output" == *"(0 sized, 1 unknown)"* ]]
}

@test "cmd_reclaim reports a TCC-denied size as unknown (no Full Disk Access) instead of the misleading partial 0" {
  mkdir -p "$TEST_DIR/tcc"
  install_size_registry "$(size_item tcc "$TEST_DIR/tcc" "echo \"du: cannot read directory '/x': Operation not permitted\" >&2; echo 0; exit 1" 'echo dry-tcc')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"tcc: size: unknown (no Full Disk Access)"* ]]
  [[ "$output" != *"tcc: size: 0K"* ]]
  [[ "$output" == *"(0 sized, 1 unknown)"* ]]
}

@test "cmd_reclaim still accepts a partial total from a non-TCC nonzero exit (plain permission-denied warnings)" {
  mkdir -p "$TEST_DIR/partial"
  install_size_registry "$(size_item partial "$TEST_DIR/partial" "echo 'du: x: Permission denied' >&2; echo 2048; exit 1" 'echo dry-partial')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"partial: size: 2.0M"* ]]
}

# macOS TCC denial in the DRY-RUN command (bead pg2-uctbw): the trash item's
# `ls -la ~/.Trash | wc -l` prints ls's "Operation not permitted" on stderr and
# wc prints a misleading "0"; under the wrapper's pipefail the pipeline exits
# non-zero. The count must never be shown as 0.
@test "cmd_reclaim dry run reports a TCC-denied dry-run command as unavailable (no Full Disk Access), not the misleading 0" {
  mkdir -p "$TEST_DIR/trashy"
  install_size_registry "$(size_item trashy "$TEST_DIR/trashy" 'echo 4' "set -o pipefail; { echo \"ls: /x/.Trash: Operation not permitted\" >&2; false; } | wc -l")"
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"trashy: size: 4K"* ]]
  [[ "$output" == *"(dry run unavailable (no Full Disk Access))"* ]]
  [[ "$output" != *"Operation not permitted"* ]]
  local zero_lines
  zero_lines=$(grep -cE '^[[:space:]]*0[[:space:]]*$' <<<"$output" || true)
  [ "$zero_lines" -eq 0 ]
  [[ "$stderr" == *"dry-run command for 'trashy' exited non-zero"* ]]
}

@test "cmd_reclaim dry run still shows a non-TCC failing dry-run command's output unchanged" {
  mkdir -p "$TEST_DIR/failing"
  install_size_registry "$(size_item failing "$TEST_DIR/failing" 'echo 4' "echo 'ls: x: Permission denied'; echo 7; exit 1")"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"ls: x: Permission denied"* ]]
  [[ "$output" == *"7"* ]]
  [[ "$output" != *"no Full Disk Access"* ]]
}

@test "cmd_reclaim dry run does not treat 'Operation not permitted' text in a SUCCESSFUL dry-run command as a TCC denial" {
  mkdir -p "$TEST_DIR/texty"
  install_size_registry "$(size_item texty "$TEST_DIR/texty" 'echo 4' "echo 'Operation not permitted is just text'")"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"Operation not permitted is just text"* ]]
  [[ "$output" != *"no Full Disk Access"* ]]
}

@test "cmd_reclaim total sums only the known sizes and counts the unknown ones" {
  mkdir -p "$TEST_DIR/a" "$TEST_DIR/b" "$TEST_DIR/c"
  install_size_registry \
    "$(size_item a "$TEST_DIR/a" 'echo 1024' 'echo dry-a')" \
    "$(size_item b "$TEST_DIR/b" 'echo 3072' 'echo dry-b')" \
    "$(size_item c "$TEST_DIR/c" 'echo nope' 'echo dry-c')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"total reclaimable: 4.0M (2 sized, 1 unknown)"* ]]
}

@test "cmd_reclaim does not size, run, or count a skipped (missing-path) item, and prints no total when nothing ran" {
  marker="$TEST_DIR/size-ran"
  install_size_registry "$(size_item gone "$TEST_DIR/does-not-exist" "touch $marker; echo 5" 'echo SHOULD-NOT-RUN')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ ! -e "$marker" ]
}

@test "cmd_reclaim mixed run: the skipped item adds no size line and does not change the total" {
  mkdir -p "$TEST_DIR/real"
  install_size_registry \
    "$(size_item gone "$TEST_DIR/does-not-exist" 'echo 9999' 'echo SHOULD-NOT-RUN')" \
    "$(size_item real "$TEST_DIR/real" 'echo 1024' 'echo dry-real')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ ! "$output" == *"gone"* ]]
  [[ "$output" == *"real: size: 1.0M"* ]]
  [[ "$output" == *"total reclaimable: 1.0M (1 sized, 0 unknown)"* ]]
}

# Long single-line dry-run output (bead pg2-es6fn amendment 2026-10-09): the
# go-build-cache item's `go clean -n -cache` prints ONE `rm -rf <256 hash
# dirs>` line. The default output collapses it to a short summary; the raw
# line stays available under -v.
install_long_line_registry() {
  mkdir -p "$TEST_DIR/gocache"
  local cmd='printf "rm -rf"; for i in $(seq 1 256); do printf " %s/gocache/%02x" "$TEST_DIR" "$i"; done; printf "\n"'
  install_size_registry "$(size_item go-build-cache "$TEST_DIR/gocache" 'echo 68500000' "$cmd")"
}

@test "cmd_reclaim collapses a very long single-line dry run to one summary with the size, not the raw rm line" {
  install_long_line_registry
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"go-build-cache: size: 65.3G"* ]]
  [[ "$output" == *"would remove 256 paths under $TEST_DIR/gocache"* ]]
  [[ ! "$output" == *"rm -rf"* ]]
  [[ ! "$output" == *"gocache/ff"* ]]
  # exactly one line names the item
  [ "$(grep -c '^go-build-cache' <<<"$output")" -eq 1 ]
}

@test "cmd_reclaim -v keeps the raw long dry-run line in addition to the summary" {
  install_long_line_registry
  run cmd_reclaim --aggressiveness 1 -v
  [ "$status" -eq 0 ]
  [[ "$output" == *"would remove 256 paths under"* ]]
  [[ "$output" == *"rm -rf"* ]]
  [[ "$output" == *"gocache/ff"* ]]
}

@test "cmd_reclaim truncates a very long non-rm dry-run line instead of echoing it" {
  mkdir -p "$TEST_DIR/wide"
  install_size_registry "$(size_item wide "$TEST_DIR/wide" 'echo 1' 'printf "X%.0s" $(seq 1 400); echo')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"[400 chars; use -v for the raw line]"* ]]
  [[ ! "$output" == *"$(printf 'X%.0s' $(seq 1 300))"* ]]
}

@test "cmd_reclaim --apply shows the size before removal and totals what was reclaimed, excluding a declined item" {
  mkdir -p "$TEST_DIR/lo" "$TEST_DIR/hi"
  install_size_registry \
    "$(size_item lo "$TEST_DIR/lo" 'echo 1024' 'echo dry-lo' 1)" \
    "$(size_item hi "$TEST_DIR/hi" 'echo 4096' 'echo dry-hi' 5)"
  pgdr_confirm() { return 1; }
  run cmd_reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"lo: size: 1.0M"* ]]
  [[ "$output" == *"remove-lo"* ]]
  [[ "$output" == *"skipping 'hi' (not confirmed)"* ]]
  [[ "$output" == *"total reclaimed: 1.0M (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim excludes an item whose command failed from the total" {
  mkdir -p "$TEST_DIR/bad" "$TEST_DIR/good"
  install_size_registry \
    "$(size_item bad "$TEST_DIR/bad" 'echo 8192' 'echo dry-bad; exit 3')" \
    "$(size_item good "$TEST_DIR/good" 'echo 1024' 'echo dry-good')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"total reclaimable: 1.0M (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim size reporting never triggers errexit in a caller running under 'set -euo pipefail' (unknown size, timeout)" {
  mkdir -p "$TEST_DIR/slow"
  install_size_registry "$(size_item slow "$TEST_DIR/slow" 'sleep 5' 'echo dry-slow')"
  PGDR_SIZE_TIMEOUT_SECONDS=1 run bash -c '
    set -euo pipefail
    source "$1/pg-disk-reclaimer.bash"
    cmd_reclaim --aggressiveness 1
    echo "AFTER"
  ' -- "$SCRIPTS_DIR"
  [ "$status" -eq 0 ]
  [[ "$output" == *"size: unknown (timed out after 1s)"* ]]
  [[ "$output" == *"AFTER"* ]]
}

@test "pgdr_validate_registry rejects a non-string or empty sizeCommand, naming the item index" {
  local reg="$TEST_DIR/bad-size.json"
  size_item x /tmp/x 'echo 1' 'echo d' | jq -s '.[0].sizeCommand = 5' >"$reg"
  run pgdr_validate_registry "$reg"
  [ "$status" -ne 0 ]
  [[ "$output" == *"index 0"* ]]
  [[ "$output" == *"sizeCommand"* ]]
  size_item x /tmp/x 'echo 1' 'echo d' | jq -s '.[0].sizeCommand = ""' >"$reg"
  run pgdr_validate_registry "$reg"
  [ "$status" -ne 0 ]
}

@test "pgdr_validate_registry accepts a registry with a valid sizeCommand and cmd_validate checks its leading command exists" {
  local reg="$TEST_DIR/size-ok.json"
  size_item x /tmp/x 'echo 1' 'true' | jq -s '.' >"$reg"
  run cmd_validate "$reg"
  [ "$status" -eq 0 ]
  size_item x /tmp/x 'pg-disk-reclaimer-test-nonexistent-cmd-xyz 1' 'true' | jq -s '.' >"$reg"
  run cmd_validate "$reg"
  [ "$status" -ne 0 ]
  [[ "$output" == *"sizeCommand"* ]]
  [[ "$output" == *"pg-disk-reclaimer-test-nonexistent-cmd-xyz"* ]]
}

# Size contract (bead pg2-0tj7h): sizeKind / sizeMethod / sizeBasis /
# heldSizeCommand. The kind is static registry metadata; sizeCommand keeps
# printing a bare KiB integer.

# with_size_fields <item-json> <jq-object-literal>: merges extra fields into
# one size_item and prints it compactly (one line, for install_size_registry).
with_size_fields() {
  jq -c --argjson extra "$2" '. + $extra' <<<"$1"
}

@test "cmd_reclaim labels an estimate with its prefix, kind and method" {
  mkdir -p "$TEST_DIR/est"
  install_size_registry "$(with_size_fields "$(size_item est "$TEST_DIR/est" 'echo 996200' 'echo dry-est')" \
    '{"sizeKind":"estimate","sizeMethod":"sqlite-closure","sizeBasis":"closure of dead paths"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"est: size: ~972.8M (estimate: sqlite-closure)"* ]]
  [[ "$output" == *"total reclaimable: ~972.8M estimate (1 estimated)"* ]]
  # sizeBasis is documentation only: never printed
  [[ ! "$output" == *"closure of dead paths"* ]]
}

@test "cmd_reclaim labels upper_bound with <= and lower_bound with >=, and a missing sizeMethod defaults to du" {
  mkdir -p "$TEST_DIR/ub" "$TEST_DIR/lb"
  install_size_registry \
    "$(with_size_fields "$(size_item ub "$TEST_DIR/ub" 'echo 1024' 'echo dry-ub')" '{"sizeKind":"upper_bound","sizeBasis":"b"}')" \
    "$(with_size_fields "$(size_item lb "$TEST_DIR/lb" 'echo 2048' 'echo dry-lb')" '{"sizeKind":"lower_bound","sizeMethod":"fetchless","sizeBasis":"b"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"ub: size: <=1.0M (upper_bound: du)"* ]]
  [[ "$output" == *"lb: size: >=2.0M (lower_bound: fetchless)"* ]]
  [[ "$output" == *"total reclaimable: <=1.0M upper_bound + >=2.0M lower_bound (1 upper_bound, 1 lower_bound)"* ]]
}

@test "cmd_reclaim gives an explicitly exact item no suffix even when it declares a sizeMethod" {
  mkdir -p "$TEST_DIR/ex"
  install_size_registry "$(with_size_fields "$(size_item ex "$TEST_DIR/ex" 'echo 1024' 'echo dry-ex')" '{"sizeKind":"exact","sizeMethod":"git-scan"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"ex: size: 1.0M"* ]]
  [[ ! "$output" == *"(exact"* ]]
  [[ "$output" == *"total reclaimable: 1.0M (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim mixed total keeps one bucket per kind, never merges them, and omits empty buckets" {
  mkdir -p "$TEST_DIR/m1" "$TEST_DIR/m2" "$TEST_DIR/m3"
  install_size_registry \
    "$(size_item m1 "$TEST_DIR/m1" 'echo 68500000' 'echo dry-m1')" \
    "$(with_size_fields "$(size_item m2 "$TEST_DIR/m2" 'echo 996200' 'echo dry-m2')" '{"sizeKind":"estimate","sizeMethod":"sqlite-closure","sizeBasis":"b"}')" \
    "$(size_item m3 "$TEST_DIR/m3" 'echo nope' 'echo dry-m3')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"m1: size: 65.3G"* ]]
  [[ "$output" == *"total reclaimable: 65.3G exact + ~972.8M estimate (1 exact, 1 estimated, 1 unknown)"* ]]
  # the buckets are not summed into one figure (65.3G + 972.8M = 66.3G)
  [[ ! "$output" == *"66.3G"* ]]
  [[ ! "$output" == *"upper_bound"* ]]
}

@test "cmd_reclaim all-exact run (even with an item-level sizeBasis present) keeps the legacy total byte-for-byte" {
  mkdir -p "$TEST_DIR/l1" "$TEST_DIR/l2"
  install_size_registry \
    "$(size_item l1 "$TEST_DIR/l1" 'echo 1024' 'echo dry-l1')" \
    "$(with_size_fields "$(size_item l2 "$TEST_DIR/l2" 'echo 3072' 'echo dry-l2')" '{"sizeBasis":"documented anyway"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'l1: size: 1.0M\ndry-l1\nl2: size: 3.0M\ndry-l2\ntotal reclaimable: 4.0M (2 sized, 0 unknown)')" ]
}

@test "cmd_reclaim prints a held line and a separate held total, never adding held into the reclaimable total" {
  mkdir -p "$TEST_DIR/h1"
  install_size_registry "$(with_size_fields "$(size_item h1 "$TEST_DIR/h1" 'echo 0' 'echo dry-h1')" '{"heldSizeCommand":"echo 4830000 extra-ignored"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"h1: size: 0K"* ]]
  [[ "$output" == *"h1: held, not removable: 4.6G"* ]]
  [[ "$output" == *"total reclaimable: 0K (1 sized, 0 unknown); held, not removable: 4.6G"* ]]
}

# Bead pg2-c8weu: an item whose dry run (or remove) command exits non-zero
# still prints its per-item size/held lines, but is NOT summed into the total
# (the run's command did not complete, so the total is not a promise about it).
# The total must say so instead of silently disagreeing with the per-item
# lines: "; N item(s) failed, excluded from total".
@test "cmd_reclaim total is marked incomplete when an item dry run fails, and excludes that item's size, held and lower_bound bucket" {
  mkdir -p "$TEST_DIR/ok" "$TEST_DIR/bad"
  install_size_registry \
    "$(with_size_fields "$(size_item ok "$TEST_DIR/ok" 'echo 1024' 'echo dry-ok')" '{"heldSizeCommand":"echo 1024"}')" \
    "$(with_size_fields "$(size_item bad "$TEST_DIR/bad" 'echo 2048' 'echo dry-bad; exit 1')" \
      '{"sizeKind":"lower_bound","sizeBasis":"b","heldSizeCommand":"echo 3072"}')"
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -ne 0 ]
  # per-item lines still show the failed item's figures
  [[ "$output" == *"bad: size: >=2.0M (lower_bound: du)"* ]]
  [[ "$output" == *"bad: held, not removable: 3.0M"* ]]
  # the total covers only the item that succeeded and says it is incomplete
  [[ "$output" == *"total reclaimable: 1.0M (1 sized, 0 unknown); held, not removable: 1.0M; 1 item(s) failed, excluded from total"* ]]
  [[ "$output" != *"lower_bound (1"* ]]
  [[ "$output" != *"4.0M"* ]]
  [[ "$stderr" == *"dry-run command for 'bad' exited non-zero"* ]]
}

@test "cmd_reclaim prints an incomplete total even when every item's dry run fails" {
  mkdir -p "$TEST_DIR/f1" "$TEST_DIR/f2"
  install_size_registry \
    "$(with_size_fields "$(size_item f1 "$TEST_DIR/f1" 'echo 1024' 'exit 1')" '{"heldSizeCommand":"echo 1024"}')" \
    "$(size_item f2 "$TEST_DIR/f2" 'echo 1024' 'exit 2')"
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"total reclaimable: 0K (0 sized, 0 unknown); 2 item(s) failed, excluded from total"* ]]
  [[ "$output" != *"held, not removable: 1.0M; "* ]]
}

@test "cmd_reclaim --apply marks the total incomplete when an item's remove command fails" {
  mkdir -p "$TEST_DIR/rok" "$TEST_DIR/rbad"
  install_size_registry \
    "$(size_item rok "$TEST_DIR/rok" 'echo 1024' 'echo dry-rok')" \
    "$(with_size_fields "$(size_item rbad "$TEST_DIR/rbad" 'echo 2048' 'echo dry-rbad')" '{"heldSizeCommand":"echo 1024"}' | jq -c '.variants[0].removeCommand = "false"')"
  run --separate-stderr cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -ne 0 ]
  [[ "$output" == *"total reclaimed: 1.0M (1 sized, 0 unknown); 1 item(s) failed, excluded from total"* ]]
  [[ "$stderr" == *"remove command for 'rbad' exited non-zero"* ]]
}

@test "cmd_reclaim held line is omitted when held is 0, and held is shown under --apply too" {
  mkdir -p "$TEST_DIR/h0" "$TEST_DIR/h2"
  install_size_registry \
    "$(with_size_fields "$(size_item h0 "$TEST_DIR/h0" 'echo 1024' 'echo dry-h0')" '{"heldSizeCommand":"echo 0"}')" \
    "$(with_size_fields "$(size_item h2 "$TEST_DIR/h2" 'echo 1024' 'echo dry-h2')" '{"heldSizeCommand":"echo 2048"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ ! "$output" == *"h0: held"* ]]
  [[ "$output" == *"h2: held, not removable: 2.0M"* ]]
  run cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"h2: held, not removable: 2.0M"* ]]
  [[ "$output" == *"total reclaimed: 2.0M (2 sized, 0 unknown); held, not removable: 2.0M"* ]]
}

@test "cmd_reclaim held size is combined with a non-exact kind in the same total line" {
  mkdir -p "$TEST_DIR/hk"
  install_size_registry "$(with_size_fields "$(size_item hk "$TEST_DIR/hk" 'echo 1024' 'echo dry-hk')" \
    '{"sizeKind":"lower_bound","sizeBasis":"b","heldSizeCommand":"echo 1024"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"total reclaimable: >=1.0M lower_bound (1 lower_bound); held, not removable: 1.0M"* ]]
}

@test "cmd_reclaim held timeout is non-fatal, marks held unknown, and never voids the main size" {
  mkdir -p "$TEST_DIR/ht"
  install_size_registry "$(with_size_fields "$(size_item ht "$TEST_DIR/ht" 'echo 1024' 'echo dry-ht')" '{"heldSizeCommand":"sleep 5"}')"
  PGDR_SIZE_TIMEOUT_SECONDS=1 run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"ht: size: 1.0M"* ]]
  [[ "$output" == *"ht: held, not removable: unknown (timed out after 1s)"* ]]
  [[ "$output" == *"total reclaimable: 1.0M (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim held failure (no number) is non-fatal and the dry run still runs" {
  mkdir -p "$TEST_DIR/hf"
  install_size_registry "$(with_size_fields "$(size_item hf "$TEST_DIR/hf" 'echo 1024' 'echo dry-hf')" '{"heldSizeCommand":"echo broken; exit 7"}')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"hf: held, not removable: unknown (size command gave no number)"* ]]
  [[ "$output" == *"dry-hf"* ]]
  [[ "$output" == *"total reclaimable: 1.0M (1 sized, 0 unknown)"* ]]
}

@test "cmd_reclaim does not run heldSizeCommand when the main size failed" {
  mkdir -p "$TEST_DIR/hn"
  local marker="$TEST_DIR/held-ran"
  install_size_registry "$(with_size_fields "$(size_item hn "$TEST_DIR/hn" 'echo nope' 'echo dry-hn')" "{\"heldSizeCommand\":\"touch $marker; echo 5\"}")"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"hn: size: unknown (size command gave no number)"* ]]
  [[ ! "$output" == *"held"* ]]
  [ ! -e "$marker" ]
}

@test "cmd_reclaim held reporting never triggers errexit in a caller running under 'set -euo pipefail' (held timeout, failure, success)" {
  mkdir -p "$TEST_DIR/e1" "$TEST_DIR/e2" "$TEST_DIR/e3"
  install_size_registry \
    "$(with_size_fields "$(size_item e1 "$TEST_DIR/e1" 'echo 1' 'echo dry-e1')" '{"heldSizeCommand":"sleep 5"}')" \
    "$(with_size_fields "$(size_item e2 "$TEST_DIR/e2" 'echo 1' 'echo dry-e2')" '{"heldSizeCommand":"false"}')" \
    "$(with_size_fields "$(size_item e3 "$TEST_DIR/e3" 'echo 1' 'echo dry-e3')" '{"heldSizeCommand":"echo 9","sizeKind":"estimate","sizeBasis":"b"}')"
  PGDR_SIZE_TIMEOUT_SECONDS=1 run bash -c '
    set -euo pipefail
    source "$1/pg-disk-reclaimer.bash"
    cmd_reclaim --aggressiveness 1
    echo "AFTER"
  ' -- "$SCRIPTS_DIR"
  [ "$status" -eq 0 ]
  [[ "$output" == *"e1: held, not removable: unknown (timed out after 1s)"* ]]
  [[ "$output" == *"e2: held, not removable: unknown (size command gave no number)"* ]]
  [[ "$output" == *"e3: held, not removable: 9K"* ]]
  [[ "$output" == *"AFTER"* ]]
}

@test "pgdr_select_variants carries sizeKind, sizeMethod and heldSizeCommand (both selection modes) but not sizeBasis" {
  local reg="$TEST_DIR/sel-size.json"
  {
    with_size_fields "$(size_item full "$TEST_DIR/full" 'echo 1' 'true')" \
      '{"sizeKind":"estimate","sizeMethod":"m","sizeBasis":"why","heldSizeCommand":"echo 2"}'
    size_item bare "$TEST_DIR/bare" '' 'true'
  } | jq -s '.' >"$reg"
  local mode
  for mode in all id; do
    if [[ $mode == all ]]; then
      run pgdr_select_variants "$reg" 1
    else
      run pgdr_select_variants "$reg" 1 full bare
    fi
    [ "$status" -eq 0 ]
    [ "$(jq -r '.[] | select(.id == "full") | .sizeKind' <<<"$output")" = "estimate" ]
    [ "$(jq -r '.[] | select(.id == "full") | .sizeMethod' <<<"$output")" = "m" ]
    [ "$(jq -r '.[] | select(.id == "full") | .heldSizeCommand' <<<"$output")" = "echo 2" ]
    [ "$(jq -r '.[] | select(.id == "full") | has("sizeBasis")' <<<"$output")" = "false" ]
    [ "$(jq -r '.[] | select(.id == "bare") | has("sizeKind") or has("sizeMethod") or has("heldSizeCommand")' <<<"$output")" = "false" ]
  done
}

# validate_with_fields <extra-json>: writes a one-item registry with the extra
# fields merged in and runs pgdr_validate_registry over it.
validate_with_fields() {
  local reg="$TEST_DIR/size-contract.json"
  with_size_fields "$(size_item x /tmp/x 'echo 1' 'true')" "$1" | jq -s '.' >"$reg"
  pgdr_validate_registry "$reg"
}

@test "pgdr_validate_registry rejects an unknown sizeKind" {
  run validate_with_fields '{"sizeKind":"approximately","sizeBasis":"b"}'
  [ "$status" -ne 0 ]
  [[ "$output" == *"index 0"* ]]
  [[ "$output" == *"sizeKind"* ]]
  run validate_with_fields '{"sizeKind":5,"sizeBasis":"b"}'
  [ "$status" -ne 0 ]
}

@test "pgdr_validate_registry rejects an empty or non-string sizeMethod, sizeBasis and heldSizeCommand" {
  local field
  for field in sizeMethod sizeBasis heldSizeCommand; do
    run validate_with_fields "{\"$field\":\"\"}"
    [ "$status" -ne 0 ]
    [[ "$output" == *"$field"* ]]
    run validate_with_fields "{\"$field\":7}"
    [ "$status" -ne 0 ]
    [[ "$output" == *"$field"* ]]
  done
}

@test "pgdr_validate_registry requires a sizeBasis whenever sizeKind is not exact" {
  local kind
  for kind in estimate upper_bound lower_bound; do
    run validate_with_fields "{\"sizeKind\":\"$kind\"}"
    [ "$status" -ne 0 ]
    [[ "$output" == *"sizeBasis"* ]]
    run validate_with_fields "{\"sizeKind\":\"$kind\",\"sizeBasis\":\"documented\"}"
    [ "$status" -eq 0 ]
  done
}

@test "pgdr_validate_registry accepts exact (explicit or default) without a sizeBasis" {
  run validate_with_fields '{"sizeKind":"exact"}'
  [ "$status" -eq 0 ]
  run validate_with_fields '{"sizeMethod":"git-scan","heldSizeCommand":"echo 0"}'
  [ "$status" -eq 0 ]
}

@test "cmd_validate flags a heldSizeCommand whose leading command does not exist" {
  local reg="$TEST_DIR/held-missing.json"
  with_size_fields "$(size_item x /tmp/x 'echo 1' 'true')" '{"heldSizeCommand":"pg-disk-reclaimer-test-nonexistent-cmd-xyz 1"}' | jq -s '.' >"$reg"
  run cmd_validate "$reg"
  [ "$status" -ne 0 ]
  [[ "$output" == *"heldSizeCommand"* ]]
  [[ "$output" == *"pg-disk-reclaimer-test-nonexistent-cmd-xyz"* ]]
}

@test "the valid.json fixture carries the size-contract fields and validates" {
  [ "$(jq -r '.[0].sizeKind' "$FIXTURES_DIR/valid.json")" = "upper_bound" ]
  [ "$(jq -r '.[0] | has("sizeBasis") and has("heldSizeCommand") and has("sizeMethod")' "$FIXTURES_DIR/valid.json")" = "true" ]
  # schema check only: cmd_validate would also need npm on PATH (a sandbox has none)
  run pgdr_validate_registry "$FIXTURES_DIR/valid.json"
  [ "$status" -eq 0 ]
}
