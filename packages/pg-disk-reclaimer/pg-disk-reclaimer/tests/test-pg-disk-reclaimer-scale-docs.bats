#!/usr/bin/env bats
# Doc-drift tests for pg-disk-reclaimer's aggressiveness scale (bead
# pg2-j12n9). docs/aggressiveness-scale.md is the single authoritative
# definition of levels 0-5 and of the interactive-confirmation gate; these
# tests keep it, the code constant (PGDR_CONFIRM_GATE_LEVEL), the live gate
# behavior, --help, and the tldr page from drifting apart.
bats_require_minimum_version 1.5.0

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
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  unset XDG_CONFIG_HOME
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

@test "behavior: at the documented gate level a declined confirmation skips removeCommand" {
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  local gate
  gate="$(doc_gate_level)"
  install_gate_registry "$gate"
  pgdr_confirm() { return 1; }

  run --separate-stderr cmd_reclaim --aggressiveness "$gate" --apply "item-$gate"
  [ "$status" -eq 0 ]
  [[ "$output" != *"remove-$gate"* ]]
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
