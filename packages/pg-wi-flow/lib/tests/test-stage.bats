#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for the lib-level primitives tc-9ddu3.1.3 adds: config.bash's
# C-2-style stage validators (pgwf_workflow_stage_exists,
# pgwf_workflow_stage_order) and tracker.bash's generic bd Adapter
# extensions (pgwf_tracker_update/create/add_dependency/relate/duplicate/
# close/note/any_children) that annotate/record-verdict/round/advance/
# create-child/merge/close/close-duplicate (pg-wi-flow.bash) are built on
# top of. `bd` itself is a mock script placed on PATH (mocks-outside-the-
# repo convention) that records every invocation to MOCK_BD_LOG and serves
# canned JSON from fixture files -- never the real remote Dolt server.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -d $LIB_PATH ]]; then
    # shellcheck disable=SC1091 # composed lib path is runtime-resolved
    source "$LIB_PATH/config.bash"
    # shellcheck disable=SC1091 # composed lib path is runtime-resolved
    source "$LIB_PATH/tracker.bash"
  else
    # nix check: LIB_PATH is colon-separated (one entry per `libraries`
    # dependency); tracker's composed .lib already has config.bash's
    # content prepended, so sourcing it alone is sufficient.
    local IFS=':'
    local -a libs
    read -ra libs <<<"$LIB_PATH"
    local lib
    for lib in "${libs[@]}"; do
      case "$lib" in
      *tracker.bash)
        # shellcheck disable=SC1090 # nix-provided composed lib path
        source "$lib"
        ;;
      esac
    done
  fi

  if ! declare -F pgwf_query_build >/dev/null 2>&1; then
    skip "tracker.bash functions not present under this library's composed LIB_PATH"
  fi

  MOCK_BIN="$(mktemp -d)"
  MOCK_BD_LOG="$(mktemp)"
  MOCK_BD_SHOW_DIR="$(mktemp -d)"
  MOCK_BD_CHILDREN_DIR="$(mktemp -d)"
  export MOCK_BD_LOG MOCK_BD_SHOW_DIR MOCK_BD_CHILDREN_DIR
  cat >"$MOCK_BIN/bd" <<'MOCKBD'
#!/usr/bin/env bash
echo "$*" >>"$MOCK_BD_LOG"
case "$1" in
show)
  id="$2"
  file="$MOCK_BD_SHOW_DIR/$id.json"
  if [[ -f $file ]]; then
    printf '{"data":[%s]}\n' "$(cat "$file")"
    exit 0
  fi
  echo "mock bd show: no fixture for $id" >&2
  exit 1
  ;;
children)
  id="$2"
  file="$MOCK_BD_CHILDREN_DIR/$id.json"
  if [[ -f $file ]]; then
    printf '{"data":%s}\n' "$(cat "$file")"
    exit 0
  fi
  printf '{"data":[]}\n'
  ;;
update)
  id="$2"
  for failing in ${MOCK_BD_UPDATE_FAIL_IDS:-}; do
    if [[ $id == "$failing" ]]; then
      echo "mock bd update: refused for $id" >&2
      exit 1
    fi
  done
  echo '{"data":[{}]}'
  ;;
create)
  if [[ -n ${MOCK_BD_CREATE_FAIL:-} ]]; then
    echo "mock bd create: forced failure" >&2
    exit 1
  fi
  id="${MOCK_BD_CREATE_ID:-tc-newchild}"
  printf '{"data":[{"id":"%s"}]}\n' "$id"
  ;;
dep)
  case "$2" in
  add)
    if [[ -n ${MOCK_BD_DEP_ADD_FAIL:-} ]]; then
      echo "mock bd dep add: forced failure" >&2
      exit 1
    fi
    echo '{"data":[{}]}'
    ;;
  relate)
    if [[ -n ${MOCK_BD_DEP_RELATE_FAIL:-} ]]; then
      echo "mock bd dep relate: forced failure" >&2
      exit 1
    fi
    echo '{"data":[{}]}'
    ;;
  *)
    echo "mock bd: unhandled dep subcommand: $2" >&2
    exit 1
    ;;
  esac
  ;;
duplicate)
  if [[ -n ${MOCK_BD_DUPLICATE_FAIL:-} ]]; then
    echo "mock bd duplicate: forced failure" >&2
    exit 1
  fi
  echo '{"data":[{}]}'
  ;;
close)
  if [[ -n ${MOCK_BD_CLOSE_FAIL:-} ]]; then
    echo "mock bd close: forced failure" >&2
    exit 1
  fi
  echo '{"data":[{}]}'
  ;;
note)
  cat >/dev/null # drain stdin (--stdin)
  if [[ -n ${MOCK_BD_NOTE_FAIL:-} ]]; then
    echo "mock bd note: forced failure" >&2
    exit 1
  fi
  echo '{"data":[{}]}'
  ;;
*)
  echo "mock bd: unhandled subcommand: $1" >&2
  exit 1
  ;;
esac
MOCKBD
  chmod +x "$MOCK_BIN/bd"
  export PATH="$MOCK_BIN:$PATH"
}

teardown() {
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_SHOW_DIR" "$MOCK_BD_CHILDREN_DIR"
}

show_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_SHOW_DIR/$id.json"
}

# --- config.bash: C-2-style stage validators ---

@test "pgwf_workflow_stage_exists: true for a present stage, false otherwise" {
  stages='{"groom":{"order":1,"entry":true},"implement":{"order":2}}'
  run pgwf_workflow_stage_exists "$stages" groom
  [ "$status" -eq 0 ]
  run pgwf_workflow_stage_exists "$stages" nope
  [ "$status" -ne 0 ]
}

@test "pgwf_workflow_stage_order: prints the stage's order, empty when absent" {
  stages='{"groom":{"order":1,"entry":true},"implement":{"order":2}}'
  run pgwf_workflow_stage_order "$stages" implement
  [ "$status" -eq 0 ]
  [ "$output" = "2" ]
  run pgwf_workflow_stage_order "$stages" nope
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# --- tracker.bash: pgwf_tracker_any_children (unfiltered by status) ---

@test "pgwf_tracker_any_children: returns every child regardless of status" {
  printf '[{"id":"tc-a","status":"open"},{"id":"tc-b","status":"closed"}]' \
    >"$MOCK_BD_CHILDREN_DIR/tc-parent.json"
  run pgwf_tracker_any_children tc-parent
  [ "$status" -eq 0 ]
  [ "$(jq 'length' <<<"$output")" -eq 2 ]
}

# --- tracker.bash: pgwf_tracker_update (generic write wrapper) ---

@test "pgwf_tracker_update: passes ARGS through with --actor and --json appended" {
  run pgwf_tracker_update tc-1 some-actor --add-label container
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--add-label container"* ]]
  [[ "$line" == *"--actor some-actor"* ]]
  [[ "$line" == *"--json"* ]]
}

@test "pgwf_tracker_update: fails loudly on a bd error" {
  export MOCK_BD_UPDATE_FAIL_IDS="tc-1"
  run pgwf_tracker_update tc-1 some-actor --add-label container
  [ "$status" -ne 0 ]
}

# --- tracker.bash: pgwf_tracker_create ---

@test "pgwf_tracker_create: prints the created issue's data[0] object" {
  export MOCK_BD_CREATE_ID="tc-newchild"
  run pgwf_tracker_create some-actor --title "a child" --parent tc-parent --no-inherit-labels
  [ "$status" -eq 0 ]
  [ "$(jq -r '.id' <<<"$output")" = "tc-newchild" ]
  grep -q -- "create --title a child --parent tc-parent --no-inherit-labels --actor some-actor --json" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_create: fails loudly on a bd error" {
  export MOCK_BD_CREATE_FAIL=1
  run pgwf_tracker_create some-actor --title "a child"
  [ "$status" -ne 0 ]
}

# --- tracker.bash: dependency/relate/duplicate/close/note wrappers ---

@test "pgwf_tracker_add_dependency: bd dep add BLOCKED BLOCKER" {
  run pgwf_tracker_add_dependency tc-child tc-blocker some-actor
  [ "$status" -eq 0 ]
  grep -q -- "dep add tc-child tc-blocker --actor some-actor" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_relate: bd dep relate ID1 ID2" {
  run pgwf_tracker_relate tc-a tc-b some-actor
  [ "$status" -eq 0 ]
  grep -q -- "dep relate tc-a tc-b --actor some-actor" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_duplicate: bd duplicate ID --of OF" {
  run pgwf_tracker_duplicate tc-dup tc-canonical some-actor
  [ "$status" -eq 0 ]
  grep -q -- "duplicate tc-dup --of tc-canonical --actor some-actor" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_duplicate: fails loudly on a bd error" {
  export MOCK_BD_DUPLICATE_FAIL=1
  run pgwf_tracker_duplicate tc-dup tc-canonical some-actor
  [ "$status" -ne 0 ]
}

@test "pgwf_tracker_close: bd close ID --reason REASON" {
  run pgwf_tracker_close tc-1 "done" some-actor
  [ "$status" -eq 0 ]
  grep -q -- "close tc-1 --reason done --actor some-actor" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_note: pipes TEXT via --stdin" {
  run pgwf_tracker_note tc-1 "merged symptoms" some-actor
  [ "$status" -eq 0 ]
  grep -q -- "note tc-1 --stdin --actor some-actor" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_note: fails loudly on a bd error" {
  export MOCK_BD_NOTE_FAIL=1
  run pgwf_tracker_note tc-1 "text" some-actor
  [ "$status" -ne 0 ]
}
