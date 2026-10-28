#!/usr/bin/env bats
# Doc-drift tests for pg-disk-reclaimer's aggressiveness scale (bead
# pg2-j12n9). docs/aggressiveness-scale.md is the single authoritative
# definition of levels 0-5 and of the interactive-confirmation gate; these
# tests keep it, the code constant (PGDR_CONFIRM_GATE_LEVEL), the live gate
# behavior, --help, and the tldr page from drifting apart.
bats_require_minimum_version 1.5.0
load 'hermetic'

setup() {
  # SCRIPTS_DIR: injected by nix check (raw src dir), or computed relative to
  # this test file for a local `bats tests/` run.
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="$SCRIPTS_DIR/pg-disk-reclaimer.sh"
  SCALE_DOC="$SCRIPTS_DIR/docs/aggressiveness-scale.md"
  DOC_PATH_FRAGMENT="docs/aggressiveness-scale.md"

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# doc_gate_level: echoes the level from the document's machine-readable marker
# line `<!-- pgdr-confirm-gate-level: N -->` (exactly one such line MUST exist).
doc_gate_level() {
  local -a hits
  mapfile -t hits < <(sed -n 's/^<!-- pgdr-confirm-gate-level: \([0-9][0-9]*\) -->$/\1/p' "$SCALE_DOC")
  [[ ${#hits[@]} -eq 1 ]] || return 1
  printf '%s\n' "${hits[0]}"
}

# install_gate_registry <level>...: writes a registry with one item per given
# aggressiveness level (id "item-<level>", its dry-run/remove commands echo a
# distinct marker), every path pointing at an existing directory.
install_gate_registry() {
  mkdir -p "$HOME/.config/pg-disk-reclaimer" "$TEST_DIR/area"
  local -a levels=("$@")
  printf '%s\n' "${levels[@]}" | jq -R -s --arg area "$TEST_DIR/area" '
    split("\n") | map(select(length > 0) | tonumber) | map({
      id: "item-\(.)",
      description: "gate probe at level \(.)",
      path: $area,
      displayCommand: "echo probe",
      variants: [{
        aggressiveness: .,
        variantDescription: "probe",
        dryRunCommand: "echo dry-\(.)",
        removeCommand: "echo remove-\(.)"
      }]
    })' >"$HOME/.config/pg-disk-reclaimer/registry.json"
}

@test "the scale document exists and carries exactly one confirm-gate marker" {
  [ -f "$SCALE_DOC" ]
  run doc_gate_level
  [ "$status" -eq 0 ]
  [[ "$output" =~ ^[0-9]+$ ]]
}

@test "the documented confirm-gate level equals the PGDR_CONFIRM_GATE_LEVEL code constant" {
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  run doc_gate_level
  [ "$status" -eq 0 ]
  [ "$output" = "$PGDR_CONFIRM_GATE_LEVEL" ]
}

@test "behavior: at the documented gate level reclaim --apply asks for confirmation; one level below it does not" {
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  local gate
  gate="$(doc_gate_level)"
  install_gate_registry "$((gate - 1))" "$gate"
  pgdr_confirm() {
    echo "CONFIRM-CALLED:$1"
    return 0
  }

  run cmd_reclaim --aggressiveness "$gate" --apply "item-$((gate - 1))"
  [ "$status" -eq 0 ]
  [[ "$output" == *"remove-$((gate - 1))"* ]]
  [[ ! "$output" =~ "CONFIRM-CALLED" ]]

  run cmd_reclaim --aggressiveness "$gate" --apply "item-$gate"
  [ "$status" -eq 0 ]
  [[ "$output" =~ "CONFIRM-CALLED" ]]
  [[ "$output" == *"remove-$gate"* ]]
}

@test "behavior: at the documented gate level a declined confirmation runs nothing and exits 2" {
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  local gate
  gate="$(doc_gate_level)"
  install_gate_registry "$((gate - 1))" "$gate"
  pgdr_confirm() { return 1; }

  run --separate-stderr cmd_reclaim --aggressiveness "$gate" --apply
  [ "$status" -eq 2 ]
  [[ "$output" != *"remove-"* ]]
}

@test "behavior: a run with several gated items asks exactly once" {
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  local gate
  gate="$(doc_gate_level)"
  install_gate_registry "$gate" "$((gate + 1))"
  pgdr_confirm() {
    echo "CONFIRM-CALLED"
    return 0
  }

  run cmd_reclaim --aggressiveness "$((gate + 1))" --apply
  [ "$status" -eq 0 ]
  [ "$(grep -c 'CONFIRM-CALLED' <<<"$output")" -eq 1 ]
}

@test "the scale document defines every level 0 through 5" {
  local n
  for n in 0 1 2 3 4 5; do
    grep -q "^### Level $n: " "$SCALE_DOC"
  done
}

@test "the scale document is written in RFC 2119 language" {
  grep -q 'RFC 2119' "$SCALE_DOC"
  grep -q '\bMUST\b' "$SCALE_DOC"
}

@test "the scale document names no ZipRecruiter-specific item (public repo)" {
  run grep -n -i -E '\bzr-|ziprecruiter|/Volumes/gitrepos' "$SCALE_DOC"
  [ "$status" -ne 0 ]
}

@test "--help points at the scale document and states the gate level" {
  run bash "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"$DOC_PATH_FRAGMENT"* ]]
  [[ "$output" == *"level >= $(doc_gate_level)"* ]]
}

@test "the tldr page points at the scale document" {
  grep -q "$DOC_PATH_FRAGMENT" "$SCRIPTS_DIR/pg-disk-reclaimer.md"
}

@test "the nix option description points at the scale document (local checkout only)" {
  local module="$SCRIPTS_DIR/../../../home/programs/pg-disk-reclaimer/default.nix"
  [[ -f $module ]] || skip "home module not reachable from the sandboxed package source"
  grep -q "$DOC_PATH_FRAGMENT" "$module"
}

# --- the documented exit statuses and the one-prompt contract (bead pg2-kb5dby) ---

# doc_marker <name>: the integer from `<!-- pgdr-confirm-<name>: N -->` (exactly one).
doc_marker() {
  local -a hits
  mapfile -t hits < <(sed -n "s/^<!-- pgdr-confirm-$1: \([0-9][0-9]*\) -->\$/\1/p" "$SCALE_DOC")
  [[ ${#hits[@]} -eq 1 ]] || return 1
  printf '%s\n' "${hits[0]}"
}

# flat <file>: the file's text with every run of whitespace squeezed to one space,
# so a phrase wrapped across lines still matches.
flat() {
  tr -s '[:space:]' ' ' <"$1"
}

@test "the documented decline and no-terminal statuses are exactly the statuses cmd_reclaim returns" {
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  local gate declined no_terminal
  gate="$(doc_gate_level)"
  declined="$(doc_marker exit-declined)"
  no_terminal="$(doc_marker exit-no-terminal)"
  install_gate_registry "$gate"

  pgdr_confirm() { return 1; }
  run cmd_reclaim --aggressiveness "$gate" --apply
  [ "$status" -eq "$declined" ]

  pgdr_confirm() { return 2; }
  run cmd_reclaim --aggressiveness "$gate" --apply
  [ "$status" -eq "$no_terminal" ]
}

@test "the two documented statuses are distinct, and neither is the generic error 1" {
  local declined no_terminal
  declined="$(doc_marker exit-declined)"
  no_terminal="$(doc_marker exit-no-terminal)"
  [ "$declined" -ne "$no_terminal" ]
  [ "$declined" -ge 2 ]
  [ "$no_terminal" -ge 2 ]
}

@test "the scale document states both statuses in prose, 'at most once', and no mid-run prompts" {
  local declined no_terminal text
  declined="$(doc_marker exit-declined)"
  no_terminal="$(doc_marker exit-no-terminal)"
  text="$(flat "$SCALE_DOC")"
  [[ $text == *"exit with status $declined"* ]]
  [[ $text == *"exit with status $no_terminal"* ]]
  [[ $text == *"AT MOST ONCE"* ]]
  [[ $text == *"MUST NOT prompt at any other point of the run"* ]]
  [[ $text == *"Commands never prompt"* ]]
}

@test "--help states both statuses, 'at most once', and no mid-run prompts" {
  local declined no_terminal text
  declined="$(doc_marker exit-declined)"
  no_terminal="$(doc_marker exit-no-terminal)"
  run bash "$SCRIPT" --help
  [ "$status" -eq 0 ]
  text="$(tr -s '[:space:]' ' ' <<<"$output")"
  [[ $text == *"(exit $declined)"* ]]
  [[ $text == *"(exit $no_terminal)"* ]]
  [[ $text == *"at most once per run"* ]]
  [[ $text == *"no command prompts mid-run"* ]]
}

@test "the tldr page states both statuses, 'at most once', and no mid-run prompts" {
  local declined no_terminal text
  declined="$(doc_marker exit-declined)"
  no_terminal="$(doc_marker exit-no-terminal)"
  text="$(flat "$SCRIPTS_DIR/pg-disk-reclaimer.md")"
  [[ $text == *"declining exits $declined"* ]]
  [[ $text == *"no terminal to ask on exits $no_terminal"* ]]
  [[ $text == *"at most once per run"* ]]
  [[ $text == *"no command prompts mid-run"* ]]
}

@test "the nix option description states both statuses, 'at most once', and no mid-run prompts (local checkout only)" {
  local module="$SCRIPTS_DIR/../../../home/programs/pg-disk-reclaimer/default.nix"
  [[ -f $module ]] || skip "home module not reachable from the sandboxed package source"
  local declined no_terminal text
  declined="$(doc_marker exit-declined)"
  no_terminal="$(doc_marker exit-no-terminal)"
  text="$(flat "$module")"
  [[ $text == *"declining exits $declined"* ]]
  [[ $text == *"no terminal to ask on exits $no_terminal"* ]]
  [[ $text == *"at most once"* ]]
  [[ $text == *"no command prompts mid-run"* ]]
}
