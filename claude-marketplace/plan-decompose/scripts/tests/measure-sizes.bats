# bats file_tags=type:unit
#
# measure-sizes.sh against the REAL script and throwaway files (bead pg2-i56s4:
# the decomposer run-report size field). Byte counts are fixed by construction.

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/measure-sizes.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
  mkdir -p "$TEST_DIR/drafts" "$TEST_DIR/packets"
  # 100 bytes exactly.
  head -c 100 /dev/zero | tr '\0' 'd' >"$TEST_DIR/design.md"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "reports design bytes, summed draft bytes, count and ratio" {
  head -c 120 /dev/zero | tr '\0' 'a' >"$TEST_DIR/drafts/a.draft.md"
  head -c 30 /dev/zero | tr '\0' 'b' >"$TEST_DIR/drafts/b.draft.md"
  run "$SCRIPT" --design "$TEST_DIR/design.md" --drafts-dir "$TEST_DIR/drafts"
  [ "$status" -eq 0 ]
  [ "$output" = "design_bytes=100 draft_bytes=150 drafts=2 bytes_per_design_byte=1.50" ]
}

@test "acceptance files are added to draft bytes but not to the draft count" {
  head -c 100 /dev/zero | tr '\0' 'a' >"$TEST_DIR/drafts/a.draft.md"
  head -c 25 /dev/zero | tr '\0' 'c' >"$TEST_DIR/packets/a.acceptance.md"
  head -c 7 /dev/zero | tr '\0' 'x' >"$TEST_DIR/packets/a.body.md"
  run "$SCRIPT" --design "$TEST_DIR/design.md" --drafts-dir "$TEST_DIR/drafts" \
    --acceptance-dir "$TEST_DIR/packets"
  [ "$status" -eq 0 ]
  [ "$output" = "design_bytes=100 draft_bytes=125 drafts=1 bytes_per_design_byte=1.25" ]
}

@test "files that are not *.draft.md are ignored" {
  head -c 40 /dev/zero | tr '\0' 'a' >"$TEST_DIR/drafts/a.draft.md"
  head -c 999 /dev/zero | tr '\0' 'z' >"$TEST_DIR/drafts/notes.txt"
  run "$SCRIPT" --design "$TEST_DIR/design.md" --drafts-dir "$TEST_DIR/drafts"
  [ "$status" -eq 0 ]
  [ "$output" = "design_bytes=100 draft_bytes=40 drafts=1 bytes_per_design_byte=0.40" ]
}

@test "a draft directory with no drafts is a usage error" {
  run "$SCRIPT" --design "$TEST_DIR/design.md" --drafts-dir "$TEST_DIR/drafts"
  [ "$status" -eq 2 ]
}

@test "an empty design is a usage error" {
  : >"$TEST_DIR/design.md"
  printf 'x\n' >"$TEST_DIR/drafts/a.draft.md"
  run "$SCRIPT" --design "$TEST_DIR/design.md" --drafts-dir "$TEST_DIR/drafts"
  [ "$status" -eq 2 ]
}

@test "missing required flags and unknown flags exit 2" {
  run "$SCRIPT" --drafts-dir "$TEST_DIR/drafts"
  [ "$status" -eq 2 ]
  run "$SCRIPT" --design "$TEST_DIR/design.md"
  [ "$status" -eq 2 ]
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
}

@test "an unreadable design exits 1" {
  printf 'x\n' >"$TEST_DIR/drafts/a.draft.md"
  run "$SCRIPT" --design "$TEST_DIR/nope.md" --drafts-dir "$TEST_DIR/drafts"
  [ "$status" -eq 1 ]
}

@test "--help exits 0 and names the output fields" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ $output == *bytes_per_design_byte* ]]
}
