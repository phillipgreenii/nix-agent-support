#!/usr/bin/env bats
# bats file_tags=type:unit
# End-to-end escalate -> context (tc-9ddu3.1.18, gap G-A of the P8 review;
# reshaped by tc-ir4en): `context` and workflow resolution MUST resolve a
# question's stage/workflow/parent from the item it was escalated for ("a
# question's stage is its parent's", design: ## State model -> Axis 2). The
# question is NOT created with `bd create --parent`: real bd 1.3.1 refuses
# the blocks edge from a parent to its own child (D-9). Instead it records the
# escalated item in `originating_item` metadata, which every parent reader
# follows. The null workflow hides a missing link (every item resolves to the
# same built-in stage), so these tests run under a MULTI-STAGE, NON-PRIMARY
# workflow and use a STATEFUL mock `bd` that really stores what `create` is
# given and answers show/children/dep from that store -- never the real
# remote Dolt server. The real-bd round trip is in
# test-pg-wi-flow-escalate-real-bd.bats.
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
  MOCK_BD_STORE="$(mktemp -d)"
  export MOCK_BD_LOG MOCK_BD_STORE
  cat >"$MOCK_BIN/bd" <<'MOCKBD'
#!/usr/bin/env bash
echo "$*" >>"$MOCK_BD_LOG"
store="$MOCK_BD_STORE"
case "$1" in
show)
  if [[ -f $store/$2.json ]]; then
    printf '{"data":[%s]}\n' "$(cat "$store/$2.json")"
  else
    echo "mock bd show: no item $2" >&2
    exit 1
  fi
  ;;
create)
  shift
  title="" parent="" labels="" metadata='{}'
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --title) title="$2"; shift 2 ;;
    --parent) parent="$2"; shift 2 ;;
    --labels) labels="$2"; shift 2 ;;
    --metadata) metadata="$2"; shift 2 ;;
    --actor) shift 2 ;;
    *) shift ;;
    esac
  done
  n=$(($(find "$store" -name 'tc-q*.json' | wc -l) + 1))
  id="tc-q$n"
  jq -cn --arg id "$id" --arg t "$title" --arg p "$parent" --arg l "$labels" --argjson m "$metadata" \
    '{id:$id,title:$t,status:"open",labels:($l|split(",")|map(select(.!=""))),metadata:$m}
     + (if $p == "" then {} else {parent:$p} end)' >"$store/$id.json"
  printf '{"data":[{"id":"%s"}]}\n' "$id"
  ;;
children)
  # bd children shows `.parent`-linked items only, like the real thing.
  jq -cs --arg p "$2" '{data: [.[] | select(.parent == $p)]}' "$store"/*.json
  ;;
dep)
  case "$2" in
  add) # dep add BLOCKED BLOCKER
    echo "$3" >>"$store/blocks-$4.up"
    echo '{"data":[{}]}'
    ;;
  list) # dep list ID --direction up : items ID blocks; --direction down : ID's blockers
    case "$*" in
    *"--direction down"* | *"--direction=down"*)
      for f in "$store"/blocks-*.up; do
        [[ -f $f ]] || continue
        if grep -qx "$3" "$f"; then
          q="${f##*/blocks-}"
          cat "$store/${q%.up}.json"
          echo
        fi
      done | jq -cs '{data: .}'
      ;;
    *)
      if [[ -f $store/blocks-$3.up ]]; then
        while read -r b; do cat "$store/$b.json"; echo; done <"$store/blocks-$3.up" | jq -cs '{data: .}'
      else
        echo '{"data":[]}'
      fi
      ;;
    esac
    ;;
  esac
  ;;
list) echo '{"data":[]}' ;;
update) echo '{"data":[{}]}' ;;
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
  mkdir -p "$HOME" "$XDG_CONFIG_HOME/pg-wi-flow"
  export HOME XDG_CONFIG_HOME
  # Two workflows; "alpha" is primary, the parent item lives in "beta"
  # (non-primary, via wi_workflow metadata) at its second stage.
  cat >"$XDG_CONFIG_HOME/pg-wi-flow/config.json" <<'CFG'
{"workflows":{
  "alpha":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"ship":{"order":2,"closes":true}}},
  "beta":{"stages":{"draft":{"order":1,"entry":true},"build":{"order":2},"verify":{"order":3,"closes":true}}}
}}
CFG
  PGWF_TEST_CWD="$TEST_DIR/work"
  mkdir -p "$PGWF_TEST_CWD"
  cd "$PGWF_TEST_CWD" || exit 1
}

teardown() {
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_STORE" "$TEST_DIR"
}

seed_item() {
  printf '%s' "$2" >"$MOCK_BD_STORE/$1.json"
}

seed_beta_parent() {
  seed_item tc-parent '{"id":"tc-parent","title":"parent item","status":"open","labels":["stage:build"],"metadata":{"wi_workflow":"beta"}}'
  seed_item tc-sib '{"id":"tc-sib","title":"sibling task","status":"open","labels":[],"metadata":{},"parent":"tc-parent"}'
}

@test "escalate: creates the question WITHOUT --parent (bd refuses a parent blocked by its child), records originating_item, adds the blocks edge" {
  seed_beta_parent
  run pgwf_cmd_escalate tc-parent --question "which host?" --trigger q:info
  [ "$status" -eq 0 ]
  create_line="$(grep '^create' "$MOCK_BD_LOG")"
  [[ "$create_line" != *"--parent"* ]]
  [ "$(jq -r '.metadata.originating_item' "$MOCK_BD_STORE/tc-q1.json")" = tc-parent ]
  [ "$(jq -r '.parent // "none"' "$MOCK_BD_STORE/tc-q1.json")" = none ]
  grep -q -- "dep add tc-parent tc-q1" "$MOCK_BD_LOG"
}

@test "escalate -> context (multi-stage, non-primary workflow): the question resolves its parent's stage, workflow, WI_PARENT, WI_SIBLINGS" {
  seed_beta_parent
  run pgwf_cmd_escalate tc-parent --question "which host?" --trigger q:info
  [ "$status" -eq 0 ]

  run pgwf_context_cmd tc-q1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_CLASS=question"* ]]
  [[ "$output" == *"WI_STAGE=build"* ]]
  [[ "$output" == *"WI_WORKFLOW=beta"* ]]
  [[ "$output" == *"WI_PARENT=tc-parent"* ]]
  [[ "$output" == *"WI_SIBLINGS="*"tc-sib:sibling task"* ]]
  [[ "$output" == *"WI_BLOCKED_PARENTS=tc-parent:parent item"* ]]
}

@test "escalate -> context: the question is not mistaken for a primary-workflow entry-stage item" {
  seed_beta_parent
  run pgwf_cmd_escalate tc-parent --question "which host?" --trigger q:info
  [ "$status" -eq 0 ]
  run pgwf_context_cmd tc-q1
  [ "$status" -eq 0 ]
  [[ "$output" != *"WI_STAGE=groom"* ]]
  [[ "$output" != *"WI_WORKFLOW=alpha"* ]]
}

@test "escalate: two questions on one parent each resolve the parent's stage, and the parent's WI_QUESTIONS lists both" {
  seed_beta_parent
  run pgwf_cmd_escalate tc-parent --question "which host?" --trigger q:info --question "which user?" --trigger q:intent
  [ "$status" -eq 0 ]
  run pgwf_context_cmd tc-q2
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_STAGE=build"* ]]
  [[ "$output" == *"WI_WORKFLOW=beta"* ]]
  [[ "$output" == *"WI_PARENT=tc-parent"* ]]

  run pgwf_context_cmd tc-parent
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_QUESTIONS="*"tc-q1:which host?"* ]]
  [[ "$output" == *"WI_QUESTIONS="*"tc-q2:which user?"* ]]
}
