#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pg-wi-flow's bd Adapter (tc-9ddu3.1.1): the config-aware
# query builder (all three shapes plus exclude_labels, per this packet's
# own mandated Validation bullet), the item-fetch/update wrappers, the
# claim/release wrappers (release clears the assignee in ONE bd call), and
# workflow inheritance (nearest ancestor, else primary, else null). `bd`
# itself is a mock script placed on PATH (per the mocks-outside-the-repo
# convention) that records every invocation to MOCK_BD_LOG and serves
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

  # lib/default.nix wires three SEPARATE mkBashLibrary derivations (actor,
  # config, tracker), each with its own check pointed at this SAME shared
  # tests/ directory (mkBashLibrary hardcodes testDir = src + "/tests" and
  # all three share src = ./.). Under actor's or config's own check,
  # LIB_PATH provides only THAT library's composed content -- tracker's
  # functions are genuinely absent there, not a bug. Skip gracefully;
  # tracker's own check (whose composed lib includes config.bash per its
  # `libraries = [ config ]`) is where this file's tests actually run.
  if ! declare -F pgwf_query_build >/dev/null 2>&1; then
    skip "tracker.bash functions not present under this library's composed LIB_PATH"
  fi

  MOCK_BIN="$(mktemp -d)"
  MOCK_BD_LOG="$(mktemp)"
  MOCK_BD_SHOW_DIR="$(mktemp -d)"
  export MOCK_BD_LOG MOCK_BD_SHOW_DIR
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
update)
  id="$2"
  case " $* " in
  *" --claim "*)
    for failing in ${MOCK_BD_CLAIM_FAIL_IDS:-}; do
      if [[ $id == "$failing" ]]; then
        echo "mock bd update: --claim refused for $id" >&2
        exit 1
      fi
    done
    ;;
  esac
  # Newer bd: reassigning an item held in_progress by another actor is
  # refused unless --force is passed.
  case " $* " in
  *" --assignee "*)
    for held in ${MOCK_BD_HELD_IDS:-}; do
      if [[ $id == "$held" && " $* " != *" --force "* ]]; then
        echo "mock bd update: cannot reassign $id: held (in_progress); pass --force" >&2
        exit 1
      fi
    done
    ;;
  esac
  for failing in ${MOCK_BD_UPDATE_FAIL_IDS:-}; do
    if [[ $id == "$failing" ]]; then
      echo "mock bd update: refused for $id" >&2
      exit 1
    fi
  done
  echo '{"data":[{}]}'
  ;;
ready | list | children | dep)
  # Row-set reads: optionally mimic bd's truncation notice on STDERR (exit 0)
  # next to a truncated pagination envelope; MOCK_BD_ROWS_FAIL makes it fail.
  if [[ -n ${MOCK_BD_ROWS_FAIL:-} ]]; then
    echo "mock bd $1: boom" >&2
    exit 1
  fi
  if [[ -n ${MOCK_BD_TRUNCATED:-} ]]; then
    echo "Showing 100 of 109 ready issues. Use --limit 0 for all, or --limit N to raise the cap." >&2
    printf '{"data":[{"id":"a"},{"id":"b"}],"pagination":{"returned":100,"total":109,"truncated":true}}\n'
  else
    printf '{"data":[{"id":"a"},{"id":"b"}]}\n'
  fi
  ;;
memories)
  if [[ -n ${MOCK_BD_MEMORIES_FAIL:-} ]]; then
    echo "mock bd memories: boom" >&2
    exit 1
  fi
  if [[ -n ${MOCK_BD_MEMORIES_FILE:-} && -f $MOCK_BD_MEMORIES_FILE ]]; then
    printf '{"data":%s}\n' "$(cat "$MOCK_BD_MEMORIES_FILE")"
  else
    printf '{"data":{}}\n'
  fi
  ;;
*)
  echo "mock bd: unhandled subcommand: $1" >&2
  exit 1
  ;;
esac
MOCKBD
  chmod +x "$MOCK_BIN/bd"
  export PATH="$MOCK_BIN:$PATH"

  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_SHOW_DIR" "$TEST_DIR"
}

show_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_SHOW_DIR/$id.json"
}

# --- pgwf_query_build: the three shapes, per config and per flag combo ---

@test "pgwf_query_build default: excludes only the human label plus exclude_labels" {
  config='{"exclude_labels":["phase","critic"]}'
  run pgwf_query_build "$config" default
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--exclude-label" ]
  [ "${lines[1]}" = "human" ]
  [ "${lines[2]}" = "--exclude-label" ]
  [ "${lines[3]}" = "phase" ]
  [ "${lines[4]}" = "--exclude-label" ]
  [ "${lines[5]}" = "critic" ]
}

@test "pgwf_query_build default: honors a configured human label" {
  config='{"labels":{"human":"needs-human"}}'
  run pgwf_query_build "$config" default
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--exclude-label" ]
  [ "${lines[1]}" = "needs-human" ]
}

@test "pgwf_query_build attended: the human label only, so legacy human items (no question label) are admitted" {
  config='{}'
  run pgwf_query_build "$config" attended
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--label" ]
  [ "${lines[1]}" = "human" ]
  [ "${#lines[@]}" -eq 2 ]
}

@test "pgwf_query_build stage: the entry stage excludes every other known stage label plus question" {
  config='{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"plan":{"order":2},"implement":{"order":3}}}}}'
  run pgwf_query_build "$config" stage groom
  [ "$status" -eq 0 ]
  joined="$(printf '%s ' "${lines[@]}")"
  [[ "$joined" == *"--exclude-label stage:plan "* ]]
  [[ "$joined" == *"--exclude-label stage:implement "* ]]
  [[ "$joined" == *"--exclude-label question "* ]]
  [[ "$joined" != *"stage:groom"* ]]
}

@test "pgwf_query_build stage: a non-entry stage is a positive --label-any" {
  config='{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"plan":{"order":2}}}}}'
  run pgwf_query_build "$config" stage plan
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--label-any" ]
  [ "${lines[1]}" = "stage:plan" ]
}

@test "pgwf_query_build stage: multiple non-entry stages are OR'd via repeated --label-any" {
  config='{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"plan":{"order":2},"implement":{"order":3}}}}}'
  run pgwf_query_build "$config" stage plan implement
  [ "$status" -eq 0 ]
  joined="$(printf '%s ' "${lines[@]}")"
  [[ "$joined" == *"--label-any stage:plan "* ]]
  [[ "$joined" == *"--label-any stage:implement "* ]]
}

@test "pgwf_query_build stage: under the null workflow, its one stage is the entry stage" {
  run pgwf_query_build '{}' stage work
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--exclude-label" ]
  [ "${lines[1]}" = "question" ]
}

@test "pgwf_query_build: every query adds exclude_labels regardless of shape" {
  config='{"exclude_labels":["merge-request"]}'
  run pgwf_query_build "$config" attended
  [ "$status" -eq 0 ]
  joined="$(printf '%s ' "${lines[@]}")"
  [[ "$joined" == *"--exclude-label merge-request "* ]]
}

# --- item-fetch wrappers ---

@test "pgwf_tracker_show_json: prints the .data[0] object" {
  show_fixture tc-1 '{"id":"tc-1","status":"open"}'
  run pgwf_tracker_show_json tc-1
  [ "$status" -eq 0 ]
  result="$(jq -r .status <<<"$output")"
  [ "$result" = "open" ]
}

@test "pgwf_tracker_item_stage: strips the configured stage_prefix" {
  show_fixture tc-1 '{"id":"tc-1","labels":["stage:implement","other"]}'
  run pgwf_tracker_item_stage '{}' tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "implement" ]
}

@test "pgwf_tracker_item_stage: empty when no stage label (entry stage)" {
  show_fixture tc-1 '{"id":"tc-1","labels":["other"]}'
  run pgwf_tracker_item_stage '{}' tc-1
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgwf_tracker_has_label: true/false" {
  show_fixture tc-1 '{"id":"tc-1","labels":["container"]}'
  run pgwf_tracker_has_label tc-1 container
  [ "$status" -eq 0 ]
  run pgwf_tracker_has_label tc-1 question
  [ "$status" -ne 0 ]
}

# --- claim / release: release clears the assignee in ONE bd call ---

@test "pgwf_tracker_try_claim: invokes bd update --claim" {
  run pgwf_tracker_try_claim tc-1 some-actor-stage
  [ "$status" -eq 0 ]
  grep -q -- "update tc-1 --claim --actor some-actor-stage" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_try_claim: propagates a lost-race failure" {
  export MOCK_BD_UPDATE_FAIL_IDS="tc-1"
  run pgwf_tracker_try_claim tc-1 some-actor-stage
  [ "$status" -ne 0 ]
}

@test "pgwf_tracker_claim_or_transfer: reserved by same session -> transfers assignee to the worker actor" {
  export MOCK_BD_CLAIM_FAIL_IDS="tc-1" MOCK_BD_HELD_IDS="tc-1"
  show_fixture tc-1 '{"id":"tc-1","assignee":"abcd1234-main-dispatcher-work"}'
  run pgwf_tracker_claim_or_transfer tc-1 abcd1234-a1-worker-work abcd1234-a1-worker
  [ "$status" -eq 0 ]
  grep -q -- "update tc-1 --assignee abcd1234-a1-worker-work --force --actor abcd1234-a1-worker-work" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_claim_or_transfer: held by a foreign session -> still fails, no transfer" {
  export MOCK_BD_CLAIM_FAIL_IDS="tc-1" MOCK_BD_HELD_IDS="tc-1"
  show_fixture tc-1 '{"id":"tc-1","assignee":"ffff0000-main-main-work"}'
  run pgwf_tracker_claim_or_transfer tc-1 abcd1234-a1-worker-work abcd1234-a1-worker
  [ "$status" -ne 0 ]
  ! grep -q -- "--assignee" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_claim_or_transfer: plain successful claim does not consult assignee" {
  run pgwf_tracker_claim_or_transfer tc-1 abcd1234-a1-worker-work abcd1234-a1-worker
  [ "$status" -eq 0 ]
  ! grep -q -- "^show" "$MOCK_BD_LOG"
}

@test "pgwf_tracker_release: clears the assignee in the SAME bd call as the status change" {
  run pgwf_tracker_release tc-1 some-actor-stage
  [ "$status" -eq 0 ]
  calls="$(grep -c '^update tc-1' "$MOCK_BD_LOG")"
  [ "$calls" -eq 1 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--status open"* ]]
  [[ "$line" == *"--assignee "* ]]
}

# --- pgwf_advance_stage: the internal stage-write primitive ---

@test "pgwf_advance_stage: removes the old stage label and adds the new one in one call" {
  show_fixture tc-1 '{"id":"tc-1","labels":["stage:groom"]}'
  run pgwf_advance_stage '{}' tc-1 implement some-actor "reason text"
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--remove-label stage:groom"* ]]
  [[ "$line" == *"--add-label stage:implement"* ]]
}

@test "pgwf_advance_stage: from the entry stage (no prior label) only adds the new label" {
  show_fixture tc-1 '{"id":"tc-1","labels":[]}'
  run pgwf_advance_stage '{}' tc-1 implement some-actor
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" != *"--remove-label"* ]]
  [[ "$line" == *"--add-label stage:implement"* ]]
}

# --- pgwf_workflow_for: nearest ancestor, else primary, else null ---

@test "pgwf_workflow_for: an item marked directly uses its own wi_workflow" {
  show_fixture tc-1 '{"id":"tc-1","metadata":{"wi_workflow":"homelab"}}'
  run pgwf_workflow_for '{}' tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "homelab" ]
}

@test "pgwf_workflow_for: inherits from the nearest ancestor that carries wi_workflow" {
  show_fixture tc-1 '{"id":"tc-1","parent":"tc-1.1","metadata":{}}'
  show_fixture tc-1.1 '{"id":"tc-1.1","parent":"tc-1.1.1","metadata":{}}'
  show_fixture tc-1.1.1 '{"id":"tc-1.1.1","metadata":{"wi_workflow":"homelab"}}'
  run pgwf_workflow_for '{}' tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "homelab" ]
}

@test "pgwf_workflow_for: an unmarked item with no ancestry falls back to the primary workflow" {
  show_fixture tc-1 '{"id":"tc-1","metadata":{}}'
  config='{"workflows":{"homelab":{"primary":true,"stages":{}}}}'
  run pgwf_workflow_for "$config" tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "homelab" ]
}

@test "pgwf_workflow_for: falls all the way back to the built-in null workflow" {
  show_fixture tc-1 '{"id":"tc-1","metadata":{}}'
  run pgwf_workflow_for '{}' tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "$PGWF_NULL_WORKFLOW_NAME" ]
}

@test "pgwf_effective_stage_name: labeled item uses its own stage label" {
  show_fixture tc-1 '{"id":"tc-1","labels":["stage:implement"],"metadata":{}}'
  run pgwf_effective_stage_name '{}' tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "implement" ]
}

@test "pgwf_effective_stage_name: an unlabeled item resolves to its workflow's entry stage" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_effective_stage_name '{}' tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "work" ]
}

# --- lessons (tc-9ddu3.2.2) ----------------------------------------------

@test "pgwf_tracker_lessons_key: lesson-<component>-<topic>" {
  run pgwf_tracker_lessons_key litellm no-retry-x
  [ "$status" -eq 0 ]
  [ "$output" = "lesson-litellm-no-retry-x" ]
}

@test "pgwf_tracker_lessons: joins matching memories with ';' and ignores other keys" {
  MOCK_BD_MEMORIES_FILE="$(mktemp)"
  export MOCK_BD_MEMORIES_FILE
  printf '%s' '{"lesson-litellm-a":"ruled out A","other-litellm-b":"unrelated","lesson-litellm-c":"line1\nline2"}' >"$MOCK_BD_MEMORIES_FILE"
  run pgwf_tracker_lessons litellm
  [ "$status" -eq 0 ]
  [ "$output" = "ruled out A;line1 line2" ]
  grep -q -- "^memories lesson-litellm- --json" "$MOCK_BD_LOG"
  rm -f "$MOCK_BD_MEMORIES_FILE"
}

@test "pgwf_tracker_lessons: empty component or no memories is empty and exit 0" {
  run pgwf_tracker_lessons ""
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  run pgwf_tracker_lessons litellm
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgwf_tracker_lessons: a failing bd memories read returns non-zero" {
  MOCK_BD_MEMORIES_FAIL=1 run pgwf_tracker_lessons litellm
  [ "$status" -ne 0 ]
}

# --- row-set reads: bd's stderr notice and 100-row cap (tc-wxvd2) ---

@test "pgwf_tracker_ready: stderr truncation notice plus truncated envelope does not break parsing" {
  MOCK_BD_TRUNCATED=1 run --separate-stderr pgwf_tracker_ready --label x
  [ "$status" -eq 0 ]
  [ "$output" = '[{"id":"a"},{"id":"b"}]' ]
}

@test "pgwf_tracker_list: stderr truncation notice plus truncated envelope does not break parsing" {
  MOCK_BD_TRUNCATED=1 run --separate-stderr pgwf_tracker_list --status open
  [ "$status" -eq 0 ]
  [ "$output" = '[{"id":"a"},{"id":"b"}]' ]
}

@test "pgwf_tracker_children/dependents: stderr notice does not break parsing" {
  MOCK_BD_TRUNCATED=1 run --separate-stderr pgwf_tracker_open_children p
  [ "$status" -eq 0 ]
  [ "$output" = '[{"id":"a"},{"id":"b"}]' ]
  MOCK_BD_TRUNCATED=1 run --separate-stderr pgwf_tracker_dependents p blocks
  [ "$status" -eq 0 ]
  [ "$output" = '[{"id":"a"},{"id":"b"}]' ]
}

@test "pgwf_tracker_ready, pgwf_tracker_list and pgwf_tracker_ready_under pass --limit 0" {
  pgwf_tracker_ready --label x >/dev/null
  pgwf_tracker_list --status open >/dev/null
  pgwf_tracker_ready_under p --label y >/dev/null
  grep -qx 'ready --label x --limit 0 --json' "$MOCK_BD_LOG"
  grep -qx 'list --status open --limit 0 --json' "$MOCK_BD_LOG"
  grep -qx 'ready --parent p --label y --limit 0 --json' "$MOCK_BD_LOG"
}

@test "pgwf_tracker_ready: a bd failure reports bd's stderr and returns non-zero" {
  MOCK_BD_ROWS_FAIL=1 run --separate-stderr pgwf_tracker_ready
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ $stderr == *"pg-wi-flow: bd ready failed"*"mock bd ready: boom"* ]]
}

# --- logical parent (tc-ir4en) -------------------------------------------

@test "pgwf_tracker_item_parent: bd .parent wins, else the originating_item metadata, else empty" {
  run pgwf_tracker_item_parent '{"id":"a","parent":"p","metadata":{"originating_item":"o"}}'
  [ "$status" -eq 0 ]
  [ "$output" = p ]
  run pgwf_tracker_item_parent '{"id":"q","metadata":{"originating_item":"o"}}'
  [ "$output" = o ]
  run pgwf_tracker_item_parent '{"id":"x","metadata":{}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  run pgwf_tracker_item_parent '{"id":"y"}'
  [ -z "$output" ]
}
