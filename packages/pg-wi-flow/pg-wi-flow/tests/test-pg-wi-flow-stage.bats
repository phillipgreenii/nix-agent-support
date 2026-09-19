#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for tc-9ddu3.1.3's item-content and stage-transition write
# verbs: annotate, record-verdict, round, advance, create-child, merge,
# close (incl. --trace), close-duplicate. `bd` is a mock script on PATH
# (mocks live OUTSIDE any git working tree per the framework's testing
# convention) that records every invocation to MOCK_BD_LOG and serves
# canned JSON from fixture files -- never the real remote Dolt server.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -n ${LIB_PATH:-} ]]; then
    local IFS=':'
    local -a libs
    read -ra libs <<<"$LIB_PATH"
    local lib
    for lib in "${libs[@]}"; do
      # shellcheck disable=SC1090 # nix-provided composed lib paths
      source "$lib"
    done
  fi
  # shellcheck disable=SC1091 # sibling file, resolved at source time
  source "$SCRIPTS_DIR/pg-wi-flow.bash"

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
  id="${MOCK_BD_CREATE_ID:-tc-newchild}"
  printf '{"data":[{"id":"%s"}]}\n' "$id"
  ;;
dep)
  echo '{"data":[{}]}'
  ;;
duplicate)
  echo '{"data":[{}]}'
  ;;
close)
  echo '{"data":[{}]}'
  ;;
note)
  cat >/dev/null # drain stdin (--stdin)
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
  unset PG_WI_FLOW_IDENT
  export PGWF_EXPLICIT_ACTOR="test-actor"

  TEST_DIR="$(mktemp -d)"
  HOME="$TEST_DIR/home"
  XDG_CONFIG_HOME="$TEST_DIR/xdg"
  mkdir -p "$HOME" "$XDG_CONFIG_HOME"
  export HOME XDG_CONFIG_HOME
  PGWF_TEST_CWD="$TEST_DIR/work"
  mkdir -p "$PGWF_TEST_CWD"
  cd "$PGWF_TEST_CWD" || exit 1
}

teardown() {
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_SHOW_DIR" "$MOCK_BD_CHILDREN_DIR" "$TEST_DIR"
}

show_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_SHOW_DIR/$id.json"
}

write_workflow_config() {
  mkdir -p "$PGWF_TEST_CWD/.claude/wi-flow"
  printf '%s' "$1" >"$PGWF_TEST_CWD/.claude/wi-flow/config.json"
}

# --- annotate ---

@test "annotate: requires at least one flag" {
  run pgwf_cmd_annotate tc-1
  [ "$status" -ne 0 ]
}

@test "annotate: --kind replaces any existing kind label" {
  show_fixture tc-1 '{"id":"tc-1","labels":["kind:bug","other"],"metadata":{}}'
  run pgwf_cmd_annotate tc-1 --kind feature
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--remove-label kind:bug"* ]]
  [[ "$line" == *"--add-label kind:feature"* ]]
}

@test "annotate: --premise stores metadata, --acceptance/--design route to native bd flags" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_annotate tc-1 --premise "https://example" --acceptance "it works" --design "approach A"
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--set-metadata wi_premise=https://example"* ]]
  [[ "$line" == *"--acceptance it works"* ]]
  [[ "$line" == *"--design approach A"* ]]
}

@test "annotate: --append-description merges onto the existing description" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{},"description":"line one"}'
  run pgwf_cmd_annotate tc-1 --append-description "line two"
  [ "$status" -eq 0 ]
  # the merged description embeds a real newline, so match the whole log
  # file rather than a single grep -- one line only under ^update tc-1.
  content="$(cat "$MOCK_BD_LOG")"
  [[ "$content" == *"--description line one"$'\n'"line two"* ]]
}

# --- record-verdict / round ---

@test "record-verdict: records the concern's verdict under the current round" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  echo '{"concern":"intent","verdict":"ready"}' >"$TEST_DIR/verdict.json"
  run pgwf_cmd_record_verdict tc-1 --concern intent --json "$TEST_DIR/verdict.json"
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *'--set-metadata wi_verdict_r0_intent={"concern":"intent","verdict":"ready"}'* ]]
}

@test "record-verdict: refuses an unknown verdict value" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  echo '{"concern":"intent","verdict":"maybe"}' >"$TEST_DIR/verdict.json"
  run pgwf_cmd_record_verdict tc-1 --concern intent --json "$TEST_DIR/verdict.json"
  [ "$status" -ne 0 ]
}

@test "record-verdict: reads --json - from stdin" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_record_verdict tc-1 --concern intent --json - <<<'{"concern":"intent","verdict":"ready"}'
  [ "$status" -eq 0 ]
}

@test "round: merges a single ready verdict into ready, no escalate" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{"wi_round":"0","wi_verdict_r0_intent":"{\"concern\":\"intent\",\"verdict\":\"ready\"}"}}'
  run pgwf_cmd_round tc-1
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "ready" ]
  [ "${#lines[@]}" -eq 1 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--set-metadata wi_round=1"* ]]
}

@test "round: a blocked concern wins over other verdicts" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{"wi_round":"0","wi_verdict_r0_intent":"{\"concern\":\"intent\",\"verdict\":\"ready\"}","wi_verdict_r0_conformance":"{\"concern\":\"conformance\",\"verdict\":\"blocked\"}"}}'
  run pgwf_cmd_round tc-1
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "blocked" ]
}

@test "round: intent duplicate verdict reports duplicate <id>" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{"wi_round":"0","wi_verdict_r0_intent":"{\"concern\":\"intent\",\"verdict\":\"duplicate\",\"of\":[\"tc-abc12\"]}"}}'
  run pgwf_cmd_round tc-1
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "duplicate tc-abc12" ]
}

@test "round: prints WI_MUST_ESCALATE=true once the counter reaches iteration_bound without ready" {
  write_workflow_config '{"iteration_bound":1}'
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{"wi_round":"0","wi_verdict_r0_intent":"{\"concern\":\"intent\",\"verdict\":\"gaps\"}"}}'
  run pgwf_cmd_round tc-1
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "gaps" ]
  [ "${lines[1]}" = "WI_MUST_ESCALATE=true" ]
}

@test "round: fails loudly when no verdicts are recorded for the current round" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_round tc-1
  [ "$status" -ne 0 ]
}

# --- advance: C-2 ---

@test "advance: any stage in the item's workflow is a valid target" {
  write_workflow_config '{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_advance tc-1 --to implement
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--add-label stage:implement"* ]]
}

@test "advance: refuses a target stage not in the item's workflow" {
  write_workflow_config '{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true}}}}}'
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_advance tc-1 --to nonexistent
  [ "$status" -ne 0 ]
}

@test "advance: a move to a lower-order stage requires --reason" {
  write_workflow_config '{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-1 '{"id":"tc-1","labels":["stage:implement"],"metadata":{}}'
  run pgwf_cmd_advance tc-1 --to groom
  [ "$status" -ne 0 ]
  run pgwf_cmd_advance tc-1 --to groom --reason "sent back"
  [ "$status" -eq 0 ]
}

@test "advance: adds the container label when the item has any children" {
  write_workflow_config '{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  printf '[{"id":"tc-child","status":"closed"}]' >"$MOCK_BD_CHILDREN_DIR/tc-1.json"
  run pgwf_cmd_advance tc-1 --to implement
  [ "$status" -eq 0 ]
  calls="$(grep -c '^update tc-1' "$MOCK_BD_LOG")"
  [ "$calls" -eq 2 ]
  grep -q -- "--add-label container" "$MOCK_BD_LOG"
}

@test "advance is the only writer of stage: labels among this packet's new CLI code" {
  # Acceptance criterion (code review/grep, not just bats): pgwf_advance_stage
  # (lib/tracker.bash) is the sole place a stage:<x> label is written;
  # advance and create-child --stage both route through it rather than
  # writing --add-label stage:... inline in pg-wi-flow.bash.
  run grep -n -- '--add-label' "$SCRIPTS_DIR/pg-wi-flow.bash"
  [ "$status" -eq 0 ]
  while IFS= read -r found_line; do
    case "$found_line" in
    *stage:*)
      echo "unexpected inline stage: label write in pg-wi-flow.bash: $found_line"
      return 1
      ;;
    esac
  done <<<"$output"
}

# --- create-child ---

@test "create-child: requires --title" {
  run pgwf_cmd_create_child tc-parent
  [ "$status" -ne 0 ]
}

@test "create-child: passes --no-inherit-labels, sets container on the parent, stays open, no supersedes" {
  export MOCK_BD_CREATE_ID="tc-newchild"
  show_fixture tc-parent '{"id":"tc-parent","labels":[],"metadata":{}}'
  run pgwf_cmd_create_child tc-parent --title "a follow-up"
  [ "$status" -eq 0 ]
  [ "$output" = "tc-newchild work $PGWF_NULL_WORKFLOW_NAME" ]

  create_line="$(grep '^create' "$MOCK_BD_LOG")"
  [[ "$create_line" == *"--no-inherit-labels"* ]]
  [[ "$create_line" != *"supersede"* ]]

  parent_update="$(grep '^update tc-parent' "$MOCK_BD_LOG")"
  [[ "$parent_update" == *"--add-label container"* ]]
  # parent is never closed -- "stays open"
  ! grep -q '^close tc-parent' "$MOCK_BD_LOG"
}

@test "create-child: --stage is validated against the child's inherited workflow, same as C-2" {
  write_workflow_config '{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-parent '{"id":"tc-parent","labels":[],"metadata":{}}'
  run pgwf_cmd_create_child tc-parent --title "bad stage" --stage nonexistent
  [ "$status" -ne 0 ]

  export MOCK_BD_CREATE_ID="tc-child2"
  show_fixture tc-child2 '{"id":"tc-child2","labels":[],"metadata":{}}'
  run pgwf_cmd_create_child tc-parent --title "land it" --kind land --stage implement --blocked-by tc-blocker
  [ "$status" -eq 0 ]
  [ "$output" = "tc-child2 implement homelab" ]
  child_advance="$(grep '^update tc-child2' "$MOCK_BD_LOG")"
  [[ "$child_advance" == *"--add-label stage:implement"* ]]
  grep -q -- "dep add tc-child2 tc-blocker" "$MOCK_BD_LOG"
}

@test "create-child: a non-entry --stage does not add a stage label when it IS the entry stage" {
  export MOCK_BD_CREATE_ID="tc-child3"
  show_fixture tc-parent '{"id":"tc-parent","labels":[],"metadata":{}}'
  run pgwf_cmd_create_child tc-parent --title "at entry" --stage work
  [ "$status" -eq 0 ]
  ! grep -q '^update tc-child3' "$MOCK_BD_LOG"
}

# --- merge ---

@test "merge: closes each id as a duplicate of the survivor, links related, notes the survivor" {
  show_fixture tc-1 '{"id":"tc-1","title":"symptom one","labels":[],"metadata":{}}'
  show_fixture tc-2 '{"id":"tc-2","title":"symptom two","labels":[],"metadata":{}}'
  show_fixture tc-survivor '{"id":"tc-survivor","labels":[],"metadata":{}}'
  run pgwf_cmd_merge tc-1 tc-2 --into tc-survivor
  [ "$status" -eq 0 ]
  grep -q -- "duplicate tc-1 --of tc-survivor" "$MOCK_BD_LOG"
  grep -q -- "duplicate tc-2 --of tc-survivor" "$MOCK_BD_LOG"
  grep -q -- "dep relate tc-1 tc-survivor" "$MOCK_BD_LOG"
  grep -q -- "dep relate tc-2 tc-survivor" "$MOCK_BD_LOG"
  grep -q -- "^note tc-survivor --stdin" "$MOCK_BD_LOG"
}

@test "merge: requires --into and at least one id" {
  run pgwf_cmd_merge --into tc-survivor
  [ "$status" -ne 0 ]
  run pgwf_cmd_merge tc-1
  [ "$status" -ne 0 ]
}

# --- close / close --trace ---

@test "close: closes with the given reason" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_close tc-1 --reason "wont-do"
  [ "$status" -eq 0 ]
  grep -q -- "close tc-1 --reason wont-do" "$MOCK_BD_LOG"
}

@test "close: requires --reason" {
  run pgwf_cmd_close tc-1
  [ "$status" -ne 0 ]
}

@test "close --trace: refuses an incomplete pointer (a bullet with no disposition)" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{},"description":"Resume:\n- first thing\n- second thing"}'
  run pgwf_cmd_close tc-1 --reason "handled" --trace "first thing=tc-2"
  [ "$status" -ne 0 ]
  ! grep -q '^close tc-1' "$MOCK_BD_LOG"
}

@test "close --trace: links a traced id, files a filed: item, closes once every bullet has a disposition" {
  export MOCK_BD_CREATE_ID="tc-filed1"
  show_fixture tc-1 '{"id":"tc-1","priority":"1","labels":[],"metadata":{},"description":"Resume:\n- first thing\n- second thing"}'
  show_fixture tc-2 '{"id":"tc-2","labels":[],"metadata":{}}'
  run pgwf_cmd_close tc-1 --reason "handled" --trace "first thing=tc-2" --trace "second thing=filed:follow up on the second thing"
  [ "$status" -eq 0 ]
  grep -q -- "dep relate tc-1 tc-2" "$MOCK_BD_LOG"
  grep -q -- "create --title follow up on the second thing --no-inherit-labels --priority 1" "$MOCK_BD_LOG"
  grep -q -- "dep relate tc-1 tc-filed1" "$MOCK_BD_LOG"
  grep -q -- "^close tc-1 --reason handled" "$MOCK_BD_LOG"
}

# --- close-duplicate: survivor rule ---

@test "close-duplicate: refuses when id is NOT newer than --of" {
  show_fixture tc-old '{"id":"tc-old","created_at":"2020-01-01T00:00:00Z","status":"open","labels":[],"metadata":{}}'
  show_fixture tc-new '{"id":"tc-new","created_at":"2020-02-01T00:00:00Z","status":"open","labels":[],"metadata":{}}'
  run pgwf_cmd_close_duplicate tc-old --of tc-new
  [ "$status" -ne 0 ]
  ! grep -q '^duplicate' "$MOCK_BD_LOG"
}

@test "close-duplicate: refuses when --of is already closed (fresh read)" {
  show_fixture tc-new '{"id":"tc-new","created_at":"2020-02-01T00:00:00Z","status":"open","labels":[],"metadata":{}}'
  show_fixture tc-old '{"id":"tc-old","created_at":"2020-01-01T00:00:00Z","status":"closed","labels":[],"metadata":{}}'
  run pgwf_cmd_close_duplicate tc-new --of tc-old
  [ "$status" -ne 0 ]
  ! grep -q '^duplicate' "$MOCK_BD_LOG"
}

@test "close-duplicate: succeeds when id is newer and --of is open, adds a related link" {
  show_fixture tc-new '{"id":"tc-new","created_at":"2020-02-01T00:00:00Z","status":"open","labels":[],"metadata":{}}'
  show_fixture tc-old '{"id":"tc-old","created_at":"2020-01-01T00:00:00Z","status":"open","labels":[],"metadata":{}}'
  run pgwf_cmd_close_duplicate tc-new --of tc-old
  [ "$status" -eq 0 ]
  grep -q -- "duplicate tc-new --of tc-old" "$MOCK_BD_LOG"
  grep -q -- "dep relate tc-new tc-old" "$MOCK_BD_LOG"
}
