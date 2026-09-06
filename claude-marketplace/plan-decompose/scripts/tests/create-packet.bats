# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/create-packet.sh"

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
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Writes a mock `bd` into $MOCK_BIN that logs every invocation's argv (one
# line per call, space-joined) to $CALL_LOG, prints $new_id on `bd create`,
# and exits 0 on `bd defer`. Any other subcommand is a hard test failure so
# a real `bd` call would never go unnoticed.
mock_bd() {
  local new_id="$1"
  cat >"$MOCK_BIN/bd" <<MOCK
#!/usr/bin/env bash
echo "\$@" >> "$CALL_LOG"
case "\$1" in
create)
  echo "$new_id"
  exit 0
  ;;
defer)
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

@test "default call passes --no-inherit-labels and prints the new id" {
  mock_bd "xyz-1.5"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 0 ]
  [ "$output" = "xyz-1.5" ]
  create_call="$(grep '^create ' "$CALL_LOG")"
  [[ "$create_call" == *"--no-inherit-labels"* ]]
  [[ "$create_call" == *"--parent xyz-1"* ]]
}

@test "defer runs immediately after create, on the new id" {
  mock_bd "xyz-1.6"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 0 ]
  defer_call="$(grep '^defer ' "$CALL_LOG")"
  [ "$defer_call" = "defer xyz-1.6" ]
}

@test "--allow-inherit-labels omits --no-inherit-labels" {
  mock_bd "xyz-1.7"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria" --allow-inherit-labels
  [ "$status" -eq 0 ]
  create_call="$(grep '^create ' "$CALL_LOG")"
  [[ "$create_call" != *"--no-inherit-labels"* ]]
}

@test "--metadata and --label are passed through" {
  mock_bd "xyz-1.8"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria" --metadata '{"pd_curated_rev":"1"}' --label extra-label
  [ "$status" -eq 0 ]
  create_call="$(grep '^create ' "$CALL_LOG")"
  [[ "$create_call" == *'--metadata {"pd_curated_rev":"1"}'* ]]
  [[ "$create_call" == *"--labels extra-label"* ]]
}

@test "--acceptance-file reads acceptance text from a file" {
  mock_bd "xyz-1.9"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 0 ]
  create_call="$(grep '^create ' "$CALL_LOG")"
  [[ "$create_call" == *"--acceptance criteria text"* ]]
}

@test "passing both --acceptance and --acceptance-file is a usage error" {
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "x" --acceptance-file "$ACCEPT_FILE"
  [ "$status" -eq 2 ]
  [[ "$output" == *"exactly one of --acceptance / --acceptance-file"* ]]
}

@test "passing neither --acceptance nor --acceptance-file is a usage error" {
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--acceptance / --acceptance-file is required"* ]]
}

@test "missing --parent is a usage error" {
  run "$SCRIPT" --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--parent is required"* ]]
}

@test "missing --title is a usage error" {
  run "$SCRIPT" --parent xyz-1 --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--title is required"* ]]
}

@test "a nonexistent --body-file is a usage error" {
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$TEST_DIR/missing.md" --acceptance "criteria"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--body-file not found"* ]]
}

@test "a nonexistent --acceptance-file is a usage error" {
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance-file "$TEST_DIR/missing.md"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--acceptance-file not found"* ]]
}

@test "an unknown option is a usage error" {
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option"* ]]
}

@test "--help exits 0 and prints usage" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
  [[ "$output" == *"create-packet.sh"* ]]
}

@test "bd not on PATH is a clear error" {
  PATH="/usr/bin:/bin"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd not found on PATH"* ]]
}

@test "a failing bd create call is a clear error" {
  cat >"$MOCK_BIN/bd" <<'MOCK'
#!/usr/bin/env bash
echo "mock bd: simulated create failure" >&2
exit 1
MOCK
  chmod +x "$MOCK_BIN/bd"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd create failed"* ]]
}

@test "a failing bd defer call is reported with the already-created id" {
  cat >"$MOCK_BIN/bd" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
create)
  echo "xyz-1.10"
  exit 0
  ;;
defer)
  echo "mock bd: simulated defer failure" >&2
  exit 1
  ;;
esac
MOCK
  chmod +x "$MOCK_BIN/bd"
  run "$SCRIPT" --parent xyz-1 --title "a packet" --body-file "$BODY_FILE" --acceptance "criteria"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd defer xyz-1.10 failed"* ]]
  [[ "$output" == *"xyz-1.10 was created but is NOT held"* ]]
}
