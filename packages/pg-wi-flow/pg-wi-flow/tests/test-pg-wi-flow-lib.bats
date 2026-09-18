#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pg-wi-flow's core CLI logic (tc-9ddu3.1.1): `next`'s
# container descent across all four documented cases (leaf / container /
# no-open-children / none-ready -- this packet's own mandated Validation
# bullet, quoted verbatim from ## Phase-1 build plan row 1's Verification
# column), and the query/list/next/claim/release subcommands end-to-end.
# `bd` is a mock script on PATH (mocks live OUTSIDE any git working tree
# per the framework's testing convention) that records every invocation to
# MOCK_BD_LOG and serves canned JSON from fixture files -- never the real
# remote Dolt server.
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
  MOCK_BD_READY_DIR="$(mktemp -d)"
  MOCK_BD_READY_PARENT_DIR="$(mktemp -d)"
  export MOCK_BD_LOG MOCK_BD_SHOW_DIR MOCK_BD_CHILDREN_DIR MOCK_BD_READY_DIR MOCK_BD_READY_PARENT_DIR
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
ready)
  parent=""
  args=("$@")
  for ((i = 0; i < ${#args[@]}; i++)); do
    if [[ "${args[$i]}" == "--parent" ]]; then
      parent="${args[$((i + 1))]}"
    fi
  done
  if [[ -n $parent ]]; then
    file="$MOCK_BD_READY_PARENT_DIR/$parent.json"
  else
    file="$MOCK_BD_READY_DIR/default.json"
  fi
  if [[ -f $file ]]; then
    printf '{"data":%s}\n' "$(cat "$file")"
  else
    printf '{"data":[]}\n'
  fi
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
list)
  file="${MOCK_BD_LIST_FILE:-}"
  if [[ -n $file && -f $file ]]; then
    cat "$file"
  else
    printf '{"data":[]}\n'
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
  unset PG_WI_FLOW_IDENT
  export PGWF_EXPLICIT_ACTOR="test-actor"

  # Isolation: keep config resolution off the real machine/repo (test
  # isolation rules -- override HOME, and run from a directory that is not
  # inside any git work tree so pgwf_config_repo_path's fallback path is
  # deterministic instead of picking up this checkout's own .claude/).
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
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_SHOW_DIR" "$MOCK_BD_CHILDREN_DIR" \
    "$MOCK_BD_READY_DIR" "$MOCK_BD_READY_PARENT_DIR" "$TEST_DIR"
}

show_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_SHOW_DIR/$id.json"
}

# --- container descent: all four documented cases ---

@test "next resolve target case 1: a leaf resolves to itself" {
  show_fixture tc-leaf '{"id":"tc-leaf","labels":[]}'
  run pgwf_next_resolve_target '{}' tc-leaf
  [ "$status" -eq 0 ]
  [ "$output" = "tc-leaf" ]
}

@test "next resolve target case 3: a childless container advances to its closing stage, then dispatches itself" {
  show_fixture tc-container '{"id":"tc-container","labels":["container","stage:groom"],"metadata":{}}'
  printf '[]' >"$MOCK_BD_CHILDREN_DIR/tc-container.json"
  config='{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"learn":{"order":2,"closes":true}}}}}'
  run pgwf_next_resolve_target "$config" tc-container
  [ "$status" -eq 0 ]
  [ "$output" = "tc-container" ]
  line="$(grep '^update tc-container' "$MOCK_BD_LOG")"
  [[ "$line" == *"--remove-label stage:groom"* ]]
  [[ "$line" == *"--add-label stage:learn"* ]]
}

@test "next resolve target case 2: a container with a ready descendant descends to it (recursively)" {
  show_fixture tc-container '{"id":"tc-container","labels":["container"],"metadata":{}}'
  show_fixture tc-leaf '{"id":"tc-leaf","labels":[]}'
  printf '[{"id":"tc-child","status":"open"}]' >"$MOCK_BD_CHILDREN_DIR/tc-container.json"
  printf '[{"id":"tc-leaf"}]' >"$MOCK_BD_READY_PARENT_DIR/tc-container.json"
  run pgwf_next_resolve_target '{}' tc-container
  [ "$status" -eq 0 ]
  [ "$output" = "tc-leaf" ]
}

@test "next resolve target case 2 recurses when the ready descendant is itself a (childless) container" {
  show_fixture tc-outer '{"id":"tc-outer","labels":["container"],"metadata":{}}'
  show_fixture tc-inner '{"id":"tc-inner","labels":["container","stage:groom"],"metadata":{}}'
  printf '[{"id":"tc-inner-child","status":"open"}]' >"$MOCK_BD_CHILDREN_DIR/tc-outer.json"
  printf '[{"id":"tc-inner"}]' >"$MOCK_BD_READY_PARENT_DIR/tc-outer.json"
  printf '[]' >"$MOCK_BD_CHILDREN_DIR/tc-inner.json"
  config='{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"learn":{"order":2,"closes":true}}}}}'
  run pgwf_next_resolve_target "$config" tc-outer
  [ "$status" -eq 0 ]
  [ "$output" = "tc-inner" ]
}

@test "next resolve target case 4: a container whose open children are all unready is skipped (empty output)" {
  show_fixture tc-container '{"id":"tc-container","labels":["container"],"metadata":{}}'
  printf '[{"id":"tc-child","status":"open"}]' >"$MOCK_BD_CHILDREN_DIR/tc-container.json"
  printf '[]' >"$MOCK_BD_READY_PARENT_DIR/tc-container.json"
  run pgwf_next_resolve_target '{}' tc-container
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# --- cmd_next: end-to-end including the iteration_bound retry ---

@test "cmd_next: none when the ready set is empty" {
  printf '[]' >"$MOCK_BD_READY_DIR/default.json"
  run pgwf_cmd_next
  [ "$status" -eq 0 ]
  [ "$output" = "none" ]
}

@test "cmd_next: claims and prints id stage workflow for a leaf" {
  printf '[{"id":"tc-leaf"}]' >"$MOCK_BD_READY_DIR/default.json"
  show_fixture tc-leaf '{"id":"tc-leaf","labels":["stage:implement"],"metadata":{}}'
  run pgwf_cmd_next
  [ "$status" -eq 0 ]
  [ "$output" = "tc-leaf implement $PGWF_NULL_WORKFLOW_NAME" ]
}

@test "cmd_next: retries the next candidate on a lost claim race, up to iteration_bound" {
  printf '[{"id":"tc-a"},{"id":"tc-b"}]' >"$MOCK_BD_READY_DIR/default.json"
  show_fixture tc-a '{"id":"tc-a","labels":[],"metadata":{}}'
  show_fixture tc-b '{"id":"tc-b","labels":[],"metadata":{}}'
  export MOCK_BD_UPDATE_FAIL_IDS="tc-a"
  run pgwf_cmd_next
  [ "$status" -eq 0 ]
  [ "$output" = "tc-b work $PGWF_NULL_WORKFLOW_NAME" ]
}

@test "cmd_next: none after exhausting iteration_bound candidates on repeated lost races" {
  mkdir -p "$XDG_CONFIG_HOME/pg-wi-flow"
  printf '{"iteration_bound":1}' >"$XDG_CONFIG_HOME/pg-wi-flow/config.json"
  printf '[{"id":"tc-a"},{"id":"tc-b"}]' >"$MOCK_BD_READY_DIR/default.json"
  show_fixture tc-a '{"id":"tc-a","labels":[],"metadata":{}}'
  show_fixture tc-b '{"id":"tc-b","labels":[],"metadata":{}}'
  export MOCK_BD_UPDATE_FAIL_IDS="tc-a tc-b"
  run pgwf_cmd_next
  [ "$status" -eq 0 ]
  [ "$output" = "none" ]
}

# --- claim / release ---

@test "cmd_claim: prints id stage workflow" {
  show_fixture tc-1 '{"id":"tc-1","labels":["stage:implement"],"metadata":{}}'
  run pgwf_cmd_claim tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "tc-1 implement $PGWF_NULL_WORKFLOW_NAME" ]
}

@test "cmd_release: clears the assignee in one bd call" {
  show_fixture tc-1 '{"id":"tc-1","labels":[],"metadata":{}}'
  run pgwf_cmd_release tc-1
  [ "$status" -eq 0 ]
  calls="$(grep -c '^update tc-1' "$MOCK_BD_LOG")"
  [ "$calls" -eq 1 ]
  line="$(grep '^update tc-1' "$MOCK_BD_LOG")"
  [[ "$line" == *"--status open"* ]]
  [[ "$line" == *"--assignee "* ]]
}

# --- query / list ---

@test "cmd_query: default shape" {
  run pgwf_cmd_query
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--exclude-label" ]
  [ "${lines[1]}" = "human" ]
}

@test "cmd_query: --attended shape" {
  run pgwf_cmd_query --attended
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "--label" ]
  [ "${lines[1]}" = "question" ]
}

@test "cmd_list: prints the ready set as JSON" {
  printf '[{"id":"tc-1"},{"id":"tc-2"}]' >"$MOCK_BD_READY_DIR/default.json"
  run pgwf_cmd_list
  [ "$status" -eq 0 ]
  result="$(jq 'length' <<<"$output")"
  [ "$result" -eq 2 ]
}
