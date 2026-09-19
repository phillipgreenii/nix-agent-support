#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for tc-9ddu3.1.4's attention-axis write verbs: escalate,
# resolve (packet Validation bullet, quoted in full: "children labels and
# the blocking edge exist BEFORE the parent is released; q:intent resolve
# refusal when actor role is not main; fingerprint reuse adds a blocking
# edge and creates nothing; --abandon closes the parent when it was the
# last open blocker"). Lives alongside test-pg-wi-flow-stage.bats and
# sources pg-wi-flow.bash the same way (SCRIPTS_DIR relative to this
# file's own package, plus nix-provided LIB_PATH under `nix flake check` --
# a relative path crossing into the sibling `lib` package does not survive
# that sandboxed build). `bd` is a mock script on PATH (mocks live OUTSIDE
# any git working tree per the framework's testing convention) that
# records every invocation to MOCK_BD_LOG and serves canned JSON from
# fixture files -- never the real remote Dolt server.
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
  MOCK_BD_DEP_DIR="$(mktemp -d)"
  MOCK_BD_LIST_FILE="$(mktemp)"
  printf '[]' >"$MOCK_BD_LIST_FILE"
  export MOCK_BD_LOG MOCK_BD_SHOW_DIR MOCK_BD_DEP_DIR MOCK_BD_LIST_FILE
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
  for failing in ${MOCK_BD_UPDATE_FAIL_IDS:-}; do
    if [[ $id == "$failing" ]]; then
      echo "mock bd update: refused for $id" >&2
      exit 1
    fi
  done
  echo '{"data":[{}]}'
  ;;
create)
  id="${MOCK_BD_CREATE_ID:-tc-newquestion}"
  printf '{"data":[{"id":"%s"}]}\n' "$id"
  ;;
list)
  printf '{"data":%s}\n' "$(cat "$MOCK_BD_LIST_FILE")"
  ;;
dep)
  sub="$2"
  case "$sub" in
  list)
    id="$3"
    direction=""
    for a in "$@"; do
      case "$a" in
      --direction=*) direction="${a#--direction=}" ;;
      esac
    done
    file="$MOCK_BD_DEP_DIR/${id}.${direction}.json"
    if [[ -f $file ]]; then
      printf '{"data":%s}\n' "$(cat "$file")"
    else
      printf '{"data":[]}\n'
    fi
    ;;
  *)
    echo '{"data":[{}]}'
    ;;
  esac
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
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_SHOW_DIR" "$MOCK_BD_DEP_DIR" "$MOCK_BD_LIST_FILE" "$TEST_DIR"
}

show_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_SHOW_DIR/$id.json"
}

dep_fixture() {
  local id="$1" direction="$2" json="$3"
  printf '%s' "$json" >"$MOCK_BD_DEP_DIR/${id}.${direction}.json"
}

# --- escalate: fingerprint dedupe (create path) ---

@test "escalate: creates a question labeled question+escalated+trigger, no-inherit-labels, fingerprint metadata set, and blocks the parent" {
  export MOCK_BD_CREATE_ID="tc-newquestion"
  show_fixture tc-parent '{"id":"tc-parent","labels":[],"metadata":{}}'
  run pgwf_cmd_escalate tc-parent --question "the host is unreachable" --trigger q:stall
  [ "$status" -eq 0 ]

  create_line="$(grep '^create' "$MOCK_BD_LOG")"
  [[ "$create_line" == *"--no-inherit-labels"* ]]
  [[ "$create_line" == *"--labels question,escalated,q:stall"* ]]
  [[ "$create_line" == *'--metadata {"fingerprint":"'* ]]

  # the blocking edge (dep add tc-parent tc-newquestion: tc-newquestion
  # blocks tc-parent) is present -- "children labels and the blocking edge
  # exist" (packet Validation bullet).
  grep -q -- "dep add tc-parent tc-newquestion" "$MOCK_BD_LOG"

  # fingerprint dedupe lookup happened before creation.
  grep -q -- "list --metadata-field fingerprint=" "$MOCK_BD_LOG"
}

@test "escalate: refuses an unknown trigger" {
  show_fixture tc-parent '{"id":"tc-parent","labels":[],"metadata":{}}'
  run pgwf_cmd_escalate tc-parent --question "text" --trigger q:bogus
  [ "$status" -ne 0 ]
  ! grep -q '^create' "$MOCK_BD_LOG"
}

@test "escalate: --question/--trigger counts must match" {
  show_fixture tc-parent '{"id":"tc-parent","labels":[],"metadata":{}}'
  run pgwf_cmd_escalate tc-parent --question "text"
  [ "$status" -ne 0 ]
}

# --- escalate: fingerprint reuse (packet Validation bullet, verbatim) ---

@test "escalate: fingerprint reuse adds a blocking edge and creates nothing" {
  printf '[{"id":"tc-existing-q"}]' >"$MOCK_BD_LIST_FILE"
  show_fixture tc-parent2 '{"id":"tc-parent2","labels":[],"metadata":{}}'
  run pgwf_cmd_escalate tc-parent2 --question "the host is unreachable" --trigger q:stall
  [ "$status" -eq 0 ]

  grep -q -- "dep add tc-parent2 tc-existing-q" "$MOCK_BD_LOG"
  ! grep -q '^create' "$MOCK_BD_LOG"
}

# --- escalate: bump mode (escalated -> human, no --question) ---

@test "escalate: with no --question, ID must already be a question" {
  show_fixture tc-notaquestion '{"id":"tc-notaquestion","labels":[],"metadata":{}}'
  run pgwf_cmd_escalate tc-notaquestion
  [ "$status" -ne 0 ]
}

@test "escalate: with no --question on an existing question, bumps escalated -> human" {
  show_fixture tc-q1 '{"id":"tc-q1","labels":["question","escalated","q:stall"],"metadata":{}}'
  run pgwf_cmd_escalate tc-q1
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-q1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--add-label human"* ]]
  [[ "$line" == *"--remove-label escalated"* ]]
}

# --- resolve: q:intent refusal (packet Validation bullet, verbatim) ---

@test "resolve --decision: refuses a q:intent question unless the actor's role is main" {
  show_fixture tc-intent-q '{"id":"tc-intent-q","labels":["question","escalated","q:intent"],"metadata":{}}'
  unset PGWF_EXPLICIT_ACTOR
  export PG_WI_FLOW_IDENT="sess1234-agentA-resolver"
  run pgwf_cmd_resolve tc-intent-q --decision approve --rationale "cited doc X"
  [ "$status" -ne 0 ]
  ! grep -q '^close tc-intent-q' "$MOCK_BD_LOG"
}

@test "resolve --decision: succeeds on a q:intent question when the actor's role is main" {
  show_fixture tc-intent-q2 '{"id":"tc-intent-q2","labels":["question","escalated","q:intent"],"metadata":{}}'
  unset PGWF_EXPLICIT_ACTOR
  export PG_WI_FLOW_IDENT="sess1234-main-main"
  run pgwf_cmd_resolve tc-intent-q2 --decision approve --rationale "cited doc X"
  [ "$status" -eq 0 ]
  grep -q -- "^close tc-intent-q2 --reason approve" "$MOCK_BD_LOG"
}

@test "resolve --decision: not refused for a non-q:intent question regardless of role" {
  show_fixture tc-info-q '{"id":"tc-info-q","labels":["question","escalated","q:info"],"metadata":{}}'
  unset PGWF_EXPLICIT_ACTOR
  export PG_WI_FLOW_IDENT="sess1234-agentA-resolver"
  run pgwf_cmd_resolve tc-info-q --decision approve --rationale "fine"
  [ "$status" -eq 0 ]
}

# --- resolve: --answer / --decision clear the attention label and close ---

@test "resolve --answer: clears the attention label and closes; parent is not touched" {
  show_fixture tc-q2 '{"id":"tc-q2","labels":["question","escalated","q:info"],"metadata":{}}'
  dep_fixture tc-q2 up '[{"id":"tc-parent3","status":"open"}]'
  run pgwf_cmd_resolve tc-q2 --answer "it is fine"
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-q2' "$MOCK_BD_LOG")"
  [[ "$line" == *"--remove-label escalated"* ]]
  grep -q -- "^close tc-q2 --reason it is fine" "$MOCK_BD_LOG"
  ! grep -q '^close tc-parent3' "$MOCK_BD_LOG"
}

@test "resolve: refuses when given zero or multiple outcomes" {
  show_fixture tc-q3 '{"id":"tc-q3","labels":["question","escalated","q:info"],"metadata":{}}'
  run pgwf_cmd_resolve tc-q3
  [ "$status" -ne 0 ]
  run pgwf_cmd_resolve tc-q3 --answer "a" --defer "+1d"
  [ "$status" -ne 0 ]
}

@test "resolve: refuses an unknown --reason-code" {
  show_fixture tc-q4 '{"id":"tc-q4","labels":["question","escalated","q:info"],"metadata":{}}'
  run pgwf_cmd_resolve tc-q4 --abandon --reason-code bogus
  [ "$status" -ne 0 ]
}

# --- resolve --abandon: last-open-blocker parent close (packet Validation
# bullet, verbatim) ---

@test "resolve --abandon: closes the parent when it was the last open blocker" {
  show_fixture tc-q5 '{"id":"tc-q5","labels":["question","escalated","q:stall"],"metadata":{}}'
  dep_fixture tc-q5 up '[{"id":"tc-parent4","status":"open"}]'
  dep_fixture tc-parent4 down '[]'
  run pgwf_cmd_resolve tc-q5 --abandon --reason-code wont-do
  [ "$status" -eq 0 ]
  grep -q -- "^close tc-q5 --reason resolve --abandon --reason-code wont-do" "$MOCK_BD_LOG"
  grep -q -- "^close tc-parent4" "$MOCK_BD_LOG"
}

@test "resolve --abandon: does NOT close the parent when another open blocker remains" {
  show_fixture tc-q6 '{"id":"tc-q6","labels":["question","escalated","q:stall"],"metadata":{}}'
  dep_fixture tc-q6 up '[{"id":"tc-parent5","status":"open"}]'
  dep_fixture tc-parent5 down '[{"id":"tc-other-blocker","status":"open"}]'
  run pgwf_cmd_resolve tc-q6 --abandon --reason-code wont-do
  [ "$status" -eq 0 ]
  grep -q -- "^close tc-q6" "$MOCK_BD_LOG"
  ! grep -q '^close tc-parent5' "$MOCK_BD_LOG"
}

@test "resolve --abandon --reason-code moot-premise: files a groom-stage follow-up on the parent" {
  export MOCK_BD_CREATE_ID="tc-cleanup"
  show_fixture tc-q7 '{"id":"tc-q7","labels":["question","escalated","q:stall"],"metadata":{}}'
  show_fixture tc-parent6 '{"id":"tc-parent6","labels":[],"metadata":{}}'
  dep_fixture tc-q7 up '[{"id":"tc-parent6","status":"open"}]'
  dep_fixture tc-parent6 down '[]'
  run pgwf_cmd_resolve tc-q7 --abandon --reason-code moot-premise
  [ "$status" -eq 0 ]
  create_line="$(grep '^create' "$MOCK_BD_LOG")"
  [[ "$create_line" == *"--parent tc-parent6"* ]]
  grep -q -- "^close tc-parent6" "$MOCK_BD_LOG"
}

# --- resolve: legacy human items ---

@test "resolve --answer on a legacy human item: appends notes, removes human, releases (stays open)" {
  show_fixture tc-legacy1 '{"id":"tc-legacy1","labels":["human"],"metadata":{}}'
  run pgwf_cmd_resolve tc-legacy1 --answer "handled by hand"
  [ "$status" -eq 0 ]
  line="$(grep '^update tc-legacy1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--append-notes handled by hand"* ]]
  [[ "$line" == *"--remove-label human"* ]]
  release_line="$(grep '^update tc-legacy1' "$MOCK_BD_LOG" | tail -1)"
  [[ "$release_line" == *"--status open"* ]]
  [[ "$release_line" == *"--assignee "* ]]
  ! grep -q '^close tc-legacy1' "$MOCK_BD_LOG"
}

@test "resolve --abandon on a legacy human item: closes" {
  show_fixture tc-legacy2 '{"id":"tc-legacy2","labels":["human"],"metadata":{}}'
  run pgwf_cmd_resolve tc-legacy2 --abandon --reason-code wont-do
  [ "$status" -eq 0 ]
  grep -q -- "^close tc-legacy2" "$MOCK_BD_LOG"
}

@test "resolve: refuses an item that is neither a question nor a legacy human item" {
  show_fixture tc-plain '{"id":"tc-plain","labels":[],"metadata":{}}'
  run pgwf_cmd_resolve tc-plain --answer "no"
  [ "$status" -ne 0 ]
}
