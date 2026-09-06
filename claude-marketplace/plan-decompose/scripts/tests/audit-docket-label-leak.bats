# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/audit-docket-label-leak.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
  MOCK_BIN="$TEST_DIR/bin"
  mkdir -p "$MOCK_BIN"
  PATH="$MOCK_BIN:$PATH"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Writes a mock `bd` into $MOCK_BIN that, on `bd list --label docket ...`,
# prints the given JSON payload (as the `.data` array) and exits 0; any
# other invocation is a hard test failure so a real `bd` call would never
# go unnoticed.
mock_bd_list() {
  local data_json="$1"
  cat >"$MOCK_BIN/bd" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "list" ]]; then
  echo '{"data": $data_json}'
  exit 0
fi
echo "mock bd: unexpected invocation: \$*" >&2
exit 99
MOCK
  chmod +x "$MOCK_BIN/bd"
}

@test "no docket-labeled beads at all is a clean run" {
  mock_bd_list '[]'
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"no leaks found"* ]]
}

@test "only epic-type docket beads is a clean run" {
  mock_bd_list '[
    {"id": "xyz-1", "issue_type": "epic", "title": "docket epic", "labels": ["docket"]},
    {"id": "xyz-2", "issue_type": "epic", "title": "phase bead", "labels": ["docket", "phase"]}
  ]'
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"no leaks found"* ]]
}

@test "a task-type bead carrying docket is flagged and exits 3" {
  mock_bd_list '[
    {"id": "xyz-1", "issue_type": "epic", "title": "docket epic", "labels": ["docket"]},
    {"id": "xyz-1.5", "issue_type": "task", "title": "leaked packet", "labels": ["docket"]}
  ]'
  run "$SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" == *"1 bead(s)"* ]]
  [[ "$output" == *"xyz-1.5 (task): leaked packet"* ]]
  [[ "$output" != *"xyz-1 "* ]]
}

@test "a bug-type bead carrying docket is flagged (matches the pg2-84o3m.31 shape)" {
  mock_bd_list '[
    {"id": "xyz-1", "issue_type": "epic", "title": "docket epic", "labels": ["docket"]},
    {"id": "xyz-1.31", "issue_type": "bug", "title": "leaked bug packet", "labels": ["docket"]}
  ]'
  run "$SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" == *"xyz-1.31 (bug): leaked bug packet"* ]]
}

@test "multiple leaks are all listed and counted" {
  mock_bd_list '[
    {"id": "xyz-1", "issue_type": "epic", "title": "docket epic", "labels": ["docket"]},
    {"id": "xyz-1.1", "issue_type": "task", "title": "leak one", "labels": ["docket"]},
    {"id": "xyz-1.2", "issue_type": "bug", "title": "leak two", "labels": ["docket"]}
  ]'
  run "$SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" == *"2 bead(s)"* ]]
  [[ "$output" == *"xyz-1.1 (task): leak one"* ]]
  [[ "$output" == *"xyz-1.2 (bug): leak two"* ]]
}

@test "--json emits a JSON array of the leaked beads only" {
  mock_bd_list '[
    {"id": "xyz-1", "issue_type": "epic", "title": "docket epic", "labels": ["docket"]},
    {"id": "xyz-1.1", "issue_type": "task", "title": "leak one", "labels": ["docket"]}
  ]'
  run "$SCRIPT" --json
  [ "$status" -eq 3 ]
  parsed_count=$(echo "$output" | jq 'length')
  [ "$parsed_count" -eq 1 ]
  parsed_id=$(echo "$output" | jq -r '.[0].id')
  [ "$parsed_id" = "xyz-1.1" ]
}

@test "--json on a clean run emits an empty JSON array and exits 0" {
  mock_bd_list '[
    {"id": "xyz-1", "issue_type": "epic", "title": "docket epic", "labels": ["docket"]}
  ]'
  run "$SCRIPT" --json
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "--help exits 0 and prints usage" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: audit-docket-label-leak.sh"* ]]
}

@test "an unknown option is a usage error" {
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option"* ]]
}

@test "a positional argument is a usage error" {
  run "$SCRIPT" extra-arg
  [ "$status" -eq 2 ]
  [[ "$output" == *"takes no positional arguments"* ]]
}

@test "bd not on PATH is a clear error" {
  PATH="/usr/bin:/bin"
  run "$SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd not found on PATH"* ]]
}

@test "a failing bd list call is a clear error, not a false clean run" {
  cat >"$MOCK_BIN/bd" <<'MOCK'
#!/usr/bin/env bash
echo "mock bd: simulated failure" >&2
exit 1
MOCK
  chmod +x "$MOCK_BIN/bd"
  run "$SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"bd list --label docket failed"* ]]
}
