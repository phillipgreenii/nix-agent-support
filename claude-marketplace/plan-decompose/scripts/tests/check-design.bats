# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/check-design.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "a fully decided design is clean" {
  cat >"$TEST_DIR/design.v3.md" <<'D'
# Widget service design v3

## Behavior

Band edges are inclusive on the lower band. The implementer chooses the Go identifiers.

## Tests

The design names three named tests:

- first
- second
- third
D
  run "$SCRIPT" --design "$TEST_DIR/design.v3.md"
  [ "$status" -eq 0 ]
  [[ "$output" == *"no findings"* ]]
}

@test "undecided seams are named, one finding per marker" {
  cat >"$TEST_DIR/design.v1.md" <<'D'
# Widget design v1

Exit codes: TBD.

The push envelope is to be decided.

The metric label set is undecided for now.
D
  run "$SCRIPT" --design "$TEST_DIR/design.v1.md"
  [ "$status" -eq 3 ]
  [[ "$output" == *"marker: line 3: Exit codes: TBD."* ]]
  [[ "$output" == *"marker: line 5: The push envelope is to be decided."* ]]
  [[ "$output" == *"marker: line 7: "* ]]
}

@test "an implementer-owned choice is not a finding" {
  printf '# D v1\n\nFile names and Go identifiers are the implementer'"'"'s choice.\n' >"$TEST_DIR/d.v1.md"
  run "$SCRIPT" --design "$TEST_DIR/d.v1.md"
  [ "$status" -eq 0 ]
}

@test "an open-items section with content is a finding; an empty one is not" {
  printf '# D v1\n\n## Open items\n\n- the reply schema\n\n## Next\n\ntext\n' >"$TEST_DIR/d.v1.md"
  run "$SCRIPT" --design "$TEST_DIR/d.v1.md"
  [ "$status" -eq 3 ]
  [[ "$output" == *"open: line 3"* ]]
  printf '# D v1\n\n## Open items\n\nNone.\n' >"$TEST_DIR/e.v1.md"
  run "$SCRIPT" --design "$TEST_DIR/e.v1.md"
  [ "$status" -eq 0 ]
}

@test "a stated count that disagrees with its list is flagged (nine named tests, ten rows)" {
  {
    printf '# D v2\n\nThe suite has nine named tests:\n\n'
    for n in 1 2 3 4 5 6 7 8 9 10; do printf -- '- test %s\n' "$n"; done
  } >"$TEST_DIR/d.v2.md"
  run "$SCRIPT" --design "$TEST_DIR/d.v2.md"
  [ "$status" -eq 3 ]
  [[ "$output" == *"count: line 3"*"nine named tests"*"10 item(s)"* ]]
}

@test "a stated count is checked against a table, header rows excluded" {
  printf '# D v2\n\nThere are 2 named checks:\n\n| name | what |\n| --- | --- |\n| a | x |\n| b | y |\n' >"$TEST_DIR/d.v2.md"
  run "$SCRIPT" --design "$TEST_DIR/d.v2.md"
  [ "$status" -eq 0 ]
}

@test "a title version that disagrees with the file name is flagged" {
  printf '# Widget design v12\n\ntext\n' >"$TEST_DIR/design.v13.md"
  run "$SCRIPT" --design "$TEST_DIR/design.v13.md"
  [ "$status" -eq 3 ]
  [[ "$output" == *"title: line 1: title says v12"*"v13"* ]]
}

@test "markers inside fenced code blocks are ignored" {
  printf '# D v1\n\n```\nTBD in a sample\n```\n' >"$TEST_DIR/d.v1.md"
  run "$SCRIPT" --design "$TEST_DIR/d.v1.md"
  [ "$status" -eq 0 ]
}

@test "usage errors" {
  run "$SCRIPT"
  [ "$status" -eq 2 ]
  run "$SCRIPT" --design "$TEST_DIR/missing.md"
  [ "$status" -eq 2 ]
}
