# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/update-packet.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
  MOCK_BIN="$TEST_DIR/bin"
  mkdir -p "$MOCK_BIN"
  PATH="$MOCK_BIN:$PATH"
  BODY_FILE="$TEST_DIR/body.md"
  echo "packet body content" >"$BODY_FILE"
  ACCEPT_FILE="$TEST_DIR/acceptance.md"
  echo "criteria text" >"$ACCEPT_FILE"
  CALL_LOG="$TEST_DIR/bd-calls.log"
  SHOW_JSON_FILE="$TEST_DIR/show.json"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Writes a mock `bd` into $MOCK_BIN that logs every invocation's argv (one
# line per call, space-joined) to $CALL_LOG, prints the contents of
# $SHOW_JSON_FILE on `bd show`, and exits 0 on `bd update`. Any other
# subcommand (create, defer, ...) exits 99 and is a test failure, so a real
# call outside the script's contract never goes unnoticed. Script logic only:
# no claim about bd's own semantics.
mock_bd() {
  cat >"$MOCK_BIN/bd" <<MOCK
#!/usr/bin/env bash
echo "\$@" >> "$CALL_LOG"
case "\$1" in
show)
  cat "$SHOW_JSON_FILE"
  exit 0
  ;;
update)
  exit 0
  ;;
*)
  echo "mock bd: unexpected invocation: \$*" >&2
  exit 99
  ;;
esac
MOCK
  chmod +x "$MOCK_BIN/bd"
}

# Stored acceptance in the envelope form: {"data": [ {...} ]} / {"data": {...}}.
show_envelope() {
  jq -n --arg a "$1" '{schema_version: 1, data: [{id: "xyz-1.5", acceptance_criteria: $a}]}' >"$SHOW_JSON_FILE"
}
show_envelope_object() {
  jq -n --arg a "$1" '{data: {id: "xyz-1.5", acceptance_criteria: $a}}' >"$SHOW_JSON_FILE"
}
# Bare-array form: [ {...} ].
show_bare_array() {
  jq -n --arg a "$1" '[{id: "xyz-1.5", acceptance_criteria: $a}]' >"$SHOW_JSON_FILE"
}
# Empty acceptance: the key is ABSENT.
show_empty() {
  echo '{"data": [{"id": "xyz-1.5", "title": "t"}]}' >"$SHOW_JSON_FILE"
}

@test "issues exactly one bd update with --body-file, plus one bd show for the stored acceptance" {
  mock_bd
  show_envelope "criteria text"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 0 ]
  [ "$output" = "xyz-1.5" ]
  [ "$(grep -c '^update ' "$CALL_LOG")" -eq 1 ]
  [ "$(grep '^update ' "$CALL_LOG")" = "update xyz-1.5 --body-file $BODY_FILE" ]
  [ "$(wc -l <"$CALL_LOG" | tr -d ' ')" -eq 2 ]
}

@test "never calls set-metadata, create or defer" {
  mock_bd
  show_envelope "criteria text"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE" --actor sess-1-role
  [ "$status" -eq 0 ]
  [[ "$(cat "$CALL_LOG")" != *"--set-metadata"* ]]
  [[ "$(cat "$CALL_LOG")" != *"--unset-metadata"* ]]
  run grep -q '^create ' "$CALL_LOG"
  [ "$status" -eq 1 ]
  run grep -q '^defer ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "--acceptance-file passes the text as --acceptance and does not read bd show" {
  mock_bd
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 0 ]
  [ "$(grep '^update ' "$CALL_LOG")" = "update xyz-1.5 --body-file $BODY_FILE --acceptance criteria text" ]
  run grep -q '^show ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "--actor reaches bd update" {
  mock_bd
  show_envelope "criteria text"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --actor sess-123-plan-decomposer
  [ "$status" -eq 0 ]
  [ "$(grep '^update ' "$CALL_LOG")" = "update xyz-1.5 --body-file $BODY_FILE --actor sess-123-plan-decomposer" ]
}

@test "without --acceptance-file the stored acceptance is left unchanged (no --acceptance flag)" {
  mock_bd
  show_envelope "criteria text"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 0 ]
  [[ "$(grep '^update ' "$CALL_LOG")" != *"--acceptance"* ]]
}

@test "rejects --metadata, --label, --allow-inherit-labels and any other flag with exit 2" {
  mock_bd
  for flag in "--metadata {}" "--label x" "--allow-inherit-labels" "--parent p" "--title t" "--acceptance a" "--status open" "--bogus"; do
    # shellcheck disable=SC2086  # word splitting of the flag/value pair is intended
    run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" $flag
    [ "$status" -eq 2 ]
    [[ "$output" == *"unknown option"* ]]
  done
  [ ! -e "$CALL_LOG" ]
}

@test "--help exits 0 and prints usage" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
  [[ "$output" == *"update-packet.sh"* ]]
  run "$SCRIPT" -h
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
}

@test "--help after other (valid) flags still exits 0 with usage and makes no bd call" {
  mock_bd
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
  [ ! -e "$CALL_LOG" ]
}

@test "missing --id is a usage error" {
  run "$SCRIPT" --body-file "$BODY_FILE"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--id is required"* ]]
}

@test "missing --body-file is a usage error" {
  run "$SCRIPT" --id xyz-1.5
  [ "$status" -eq 2 ]
  [[ "$output" == *"--body-file is required"* ]]
}

@test "a nonexistent --body-file is a usage error" {
  run "$SCRIPT" --id xyz-1.5 --body-file "$TEST_DIR/missing.md"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--body-file not found"* ]]
}

@test "a nonexistent --acceptance-file is a usage error" {
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$TEST_DIR/missing.md"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--acceptance-file not found"* ]]
}

@test "a flag without its value is a usage error" {
  run "$SCRIPT" --id
  [ "$status" -eq 2 ]
  [[ "$output" == *"--id requires a value"* ]]
}

@test "duplicate acceptance block vs --acceptance-file is refused with exit 3 and no bd update" {
  mock_bd
  printf 'obj\n- [ ] first criterion\n- [ ] second criterion\n' >"$BODY_FILE"
  printf -- '- [ ] first criterion\n- [ ] second criterion\n' >"$ACCEPT_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 3 ]
  [[ "$output" == *"first criterion"* ]]
  [ ! -e "$CALL_LOG" ]
}

@test "one matching line vs --acceptance-file passes" {
  mock_bd
  printf 'obj\nfirst criterion\n' >"$BODY_FILE"
  printf -- '- [ ] first criterion\n- [ ] second criterion\n' >"$ACCEPT_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 0 ]
}

@test "heading + checklist is refused with exit 3 and no bd update" {
  mock_bd
  show_envelope "something else"
  printf '## Acceptance criteria\n- [ ] a\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 3 ]
  run grep -q '^update ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "stored acceptance (envelope, array data) is guarded against" {
  mock_bd
  show_envelope "$(printf -- '- [ ] first criterion\n- [ ] second criterion')"
  printf 'x\nfirst criterion\nsecond criterion\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 3 ]
  grep -q '^show xyz-1.5 --json$' "$CALL_LOG"
  run grep -q '^update ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "stored acceptance (envelope, object data) is guarded against" {
  mock_bd
  show_envelope_object "$(printf -- '- [ ] first criterion\n- [ ] second criterion')"
  printf 'x\nfirst criterion\nsecond criterion\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 3 ]
}

@test "stored acceptance (bare array) is guarded against" {
  mock_bd
  show_bare_array "$(printf -- '- [ ] first criterion\n- [ ] second criterion')"
  printf 'x\nfirst criterion\nsecond criterion\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 3 ]
  run grep -q '^update ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "stored acceptance (bare array), clean body passes" {
  mock_bd
  show_bare_array "$(printf -- '- [ ] first criterion\n- [ ] second criterion')"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 0 ]
  grep -q '^update ' "$CALL_LOG"
}

@test "empty stored acceptance (key absent): a clean body passes" {
  mock_bd
  show_empty
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 0 ]
  grep -q '^update ' "$CALL_LOG"
}

@test "empty stored acceptance (key absent): heading + checklist is still refused" {
  mock_bd
  show_empty
  printf 'Acceptance criteria\n\n- [ ] a\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 3 ]
}

@test "empty stored acceptance (key absent): blank body lines never count as matches" {
  mock_bd
  show_empty
  printf 'a\n\n\nb\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 0 ]
}

@test "a failing bd show is a clear error and no update runs" {
  cat >"$MOCK_BIN/bd" <<'MOCK'
#!/usr/bin/env bash
echo "$@" >> "$CALL_LOG_PATH"
echo "mock bd: simulated show failure" >&2
exit 1
MOCK
  chmod +x "$MOCK_BIN/bd"
  CALL_LOG_PATH="$CALL_LOG" run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd show xyz-1.5 failed"* ]]
  run grep -q '^update ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "a failing bd update is a clear error" {
  cat >"$MOCK_BIN/bd" <<MOCK
#!/usr/bin/env bash
case "\$1" in
show) echo '[{"id":"xyz-1.5"}]'; exit 0 ;;
update) echo "mock bd: simulated update failure" >&2; exit 1 ;;
esac
MOCK
  chmod +x "$MOCK_BIN/bd"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd update xyz-1.5 failed"* ]]
}

@test "bd not on PATH is a clear error" {
  PATH="/usr/bin:/bin"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd not found on PATH"* ]]
}

# --- review follow-ups -------------------------------------------------------

# A PATH whose awk always fails: the guard must not fail open.
failing_awk() {
  printf '#!/usr/bin/env bash\nexit 2\n' >"$MOCK_BIN/awk"
  chmod +x "$MOCK_BIN/awk"
}

@test "an awk failure never passes the guard: hard error, no bd update" {
  mock_bd
  show_envelope "criteria text"
  failing_awk
  printf 'obj\n- [ ] a\n- [ ] b\n' >"$BODY_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 1 ]
  [[ "$output" == *"acceptance guard"* ]]
  [ ! -e "$CALL_LOG" ]
}

@test "an awk failure also stops the stored-acceptance path before bd update" {
  mock_bd
  show_envelope "criteria text"
  failing_awk
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE"
  [ "$status" -eq 1 ]
  run grep -q '^update ' "$CALL_LOG"
  [ "$status" -eq 1 ]
}

@test "an --id starting with '-' is a usage error even with --acceptance-file" {
  mock_bd
  run "$SCRIPT" --id --set-metadata --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--id must not start with '-'"* ]]
  [ ! -e "$CALL_LOG" ]
}

@test "an empty --acceptance-file is a usage error (it would clear the stored acceptance)" {
  mock_bd
  : >"$ACCEPT_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--acceptance-file is empty"* ]]
  [ ! -e "$CALL_LOG" ]
}

@test "a whitespace-only --acceptance-file is a usage error" {
  mock_bd
  printf '  \n\n' >"$ACCEPT_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 2 ]
  [ ! -e "$CALL_LOG" ]
}

@test "generic acceptance lines (fences, rules) are not matched: only one real line shared passes" {
  mock_bd
  printf 'obj\n```\n---\nreal criterion\n```\n---\n' >"$BODY_FILE"
  printf -- '```\n---\n- [ ] real criterion\n- [ ] another real criterion\n```\n---\n' >"$ACCEPT_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 0 ]
  grep -q '^update ' "$CALL_LOG"
}

@test "generic lines do not count toward the 2-line threshold, real lines still do" {
  mock_bd
  printf 'obj\n```\nreal one\nreal two\n' >"$BODY_FILE"
  printf -- '```\n---\nreal one\nreal two\n' >"$ACCEPT_FILE"
  run "$SCRIPT" --id xyz-1.5 --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 3 ]
  [[ "$output" == *"2 acceptance lines"* ]]
}
