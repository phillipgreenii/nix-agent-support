#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pg-wi-flow's render engine (tc-9ddu3.1.2): config
# layering as read by context (repo overlay beats machine layer), a
# question item's stage following its parent's (Axis 2), WI_KINDS/WI_TOPICS
# rendered with descriptions only at the classification (entry) stage,
# WI_APPLICABLE_DOCS populated from fixture docs, and stale-premise
# flagging [design: ## Phase-1 build plan, row 2 Verification column,
# verbatim]. `bd` is a mock script on PATH (mocks live OUTSIDE any git
# working tree) that records every invocation to MOCK_BD_LOG and serves
# canned JSON from fixture files -- never the real remote Dolt server.
bats_require_minimum_version 1.5.0

load_git_fixture_harness() {
  if [[ -n ${TEST_SUPPORT:-} ]]; then
    # shellcheck disable=SC1091 # nix-provided test-support path
    source "$TEST_SUPPORT/git-fixture-harness.bash"
  else
    # shellcheck disable=SC1091 # sibling test-support dir, resolved at source time
    source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../test-support" && pwd)/git-fixture-harness.bash"
  fi
}

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -d $LIB_PATH ]]; then
    # shellcheck disable=SC1091 # composed lib path is runtime-resolved
    source "$LIB_PATH/config.bash"
    # shellcheck disable=SC1091 # composed lib path is runtime-resolved
    source "$LIB_PATH/tracker.bash"
    # shellcheck disable=SC1091 # composed lib path is runtime-resolved
    source "$LIB_PATH/context.bash"
  else
    # nix check: LIB_PATH is colon-separated (one entry per `libraries`
    # dependency); context's composed .lib already has config.bash's and
    # tracker.bash's content prepended, so sourcing it alone is sufficient.
    local IFS=':'
    local -a libs
    read -ra libs <<<"$LIB_PATH"
    local lib
    for lib in "${libs[@]}"; do
      case "$lib" in
      *context.bash)
        # shellcheck disable=SC1090 # nix-provided composed lib path
        source "$lib"
        ;;
      esac
    done
  fi

  # lib/default.nix wires FOUR separate mkBashLibrary derivations (actor,
  # config, tracker, context), each with its own check pointed at this
  # SAME shared tests/ directory (mkBashLibrary hardcodes testDir = src +
  # "/tests" and all four share src = ./.). Under actor's/config's/
  # tracker's own check, LIB_PATH provides only that library's composed
  # content -- context's functions are genuinely absent there, not a bug.
  # Skip gracefully; context's own check (whose composed lib includes
  # config.bash and tracker.bash per its `libraries = [ tracker ]`) is
  # where this file's tests actually run.
  if ! declare -F pgwf_context_cmd >/dev/null 2>&1; then
    skip "context.bash functions not present under this library's composed LIB_PATH"
  fi

  MOCK_BIN="$(mktemp -d)"
  MOCK_BD_LOG="$(mktemp)"
  MOCK_BD_SHOW_DIR="$(mktemp -d)"
  MOCK_BD_LIST_DIR="$(mktemp -d)"
  export MOCK_BD_LOG MOCK_BD_SHOW_DIR MOCK_BD_LIST_DIR
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
  file="$MOCK_BD_LIST_DIR/children-$id.json"
  if [[ -f $file ]]; then
    printf '{"data":%s}\n' "$(cat "$file")"
  else
    printf '{"data":[]}\n'
  fi
  ;;
list)
  # A key derived from the label/metadata-field arg (if any) selects a
  # canned fixture file; anything unrecognized (or no fixture present)
  # returns an empty result set -- most tests need no list data at all.
  key=""
  args=("$@")
  for ((i = 0; i < ${#args[@]}; i++)); do
    case "${args[$i]}" in
    --label-any) key="${args[$((i + 1))]}" ;;
    esac
  done
  file="$MOCK_BD_LIST_DIR/list-${key}.json"
  if [[ -n $key && -f $file ]]; then
    printf '{"data":%s}\n' "$(cat "$file")"
  else
    printf '{"data":[]}\n'
  fi
  ;;
dep)
  printf '{"data":[]}\n'
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
  HOME="$TEST_DIR/home"
  XDG_CONFIG_HOME="$TEST_DIR/xdg"
  mkdir -p "$HOME" "$XDG_CONFIG_HOME"
  export HOME XDG_CONFIG_HOME
}

teardown() {
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$MOCK_BD_SHOW_DIR" "$MOCK_BD_LIST_DIR" "$TEST_DIR"
  if [[ -n ${GFH_ROOT:-} ]]; then
    load_git_fixture_harness
    gfh_teardown
  fi
}

show_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_SHOW_DIR/$id.json"
}

list_fixture() {
  local key="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_LIST_DIR/list-${key}.json"
}

children_fixture() {
  local id="$1" json="$2"
  printf '%s' "$json" >"$MOCK_BD_LIST_DIR/children-${id}.json"
}

# repo_with_config CONFIG_JSON -- a hermetic throwaway git repo (git
# fixture harness) with CONFIG_JSON written as its .claude/wi-flow/
# config.json overlay, cd'ed into. Sets GFH_REPO. gfh_setup's own
# gfh_reset_env scrubs every exported variable not on its allowlist --
# including this file's MOCK_BD_*/HOME/XDG_CONFIG_HOME set up in setup()
# -- so those are captured as locals first and re-exported afterward, per
# the harness's own documented contract ("a caller needing anything
# beyond this minimal set exports it AFTER this call returns").
repo_with_config() {
  local config_json="$1"
  local saved_log="$MOCK_BD_LOG" saved_show="$MOCK_BD_SHOW_DIR" saved_list="$MOCK_BD_LIST_DIR"
  local saved_home="$HOME" saved_xdg="$XDG_CONFIG_HOME"
  load_git_fixture_harness
  gfh_setup "pg-wi-flow-context"
  MOCK_BD_LOG="$saved_log"
  MOCK_BD_SHOW_DIR="$saved_show"
  MOCK_BD_LIST_DIR="$saved_list"
  HOME="$saved_home"
  XDG_CONFIG_HOME="$saved_xdg"
  export MOCK_BD_LOG MOCK_BD_SHOW_DIR MOCK_BD_LIST_DIR HOME XDG_CONFIG_HOME
  mkdir -p "$GFH_REPO/.claude/wi-flow"
  printf '%s' "$config_json" >"$GFH_REPO/.claude/wi-flow/config.json"
  cd "$GFH_REPO" || return 1
}

# --- overlay beats default (config layering as read by context) --------

@test "context: the repo overlay's kinds description wins over the machine layer's" {
  mkdir -p "$XDG_CONFIG_HOME/pg-wi-flow"
  printf '{"kinds":{"bug":"machine desc"}}' >"$XDG_CONFIG_HOME/pg-wi-flow/config.json"
  repo_with_config '{"kinds":{"bug":"repo desc"},"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":[]}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"a bug","labels":["kind:bug"],"metadata":{}}'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_KINDS=bug: repo desc"* ]]
  [[ "$output" != *"machine desc"* ]]
}

# --- question follows parent ---------------------------------------------

@test "context: a question item's WI_STAGE renders as its parent's stage" {
  repo_with_config '{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-parent '{"id":"tc-parent","title":"parent item","labels":["stage:implement"],"metadata":{}}'
  show_fixture tc-q '{"id":"tc-q","title":"a question","parent":"tc-parent","labels":["question"],"metadata":{}}'
  run pgwf_context_cmd tc-q
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_CLASS=question"* ]]
  [[ "$output" == *"WI_STAGE=implement"* ]]
}

# --- kinds/topics rendered with descriptions, classification stage only -

@test "context: WI_KINDS/WI_TOPICS render with descriptions at the classification (entry) stage" {
  repo_with_config '{"kinds":{"bug":"something is broken"},"topics":{"touches-auth":"changes auth"},"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"item","labels":["stage:groom"],"metadata":{}}'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_KINDS=bug: something is broken"* ]]
  [[ "$output" == *"WI_TOPICS=touches-auth: changes auth"* ]]
}

@test "context: WI_KINDS/WI_TOPICS are empty at a non-classification stage" {
  repo_with_config '{"kinds":{"bug":"something is broken"},"topics":{"touches-auth":"changes auth"},"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"item","labels":["stage:implement"],"metadata":{}}'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -qxF 'WI_KINDS='
  printf '%s\n' "$output" | grep -qxF 'WI_TOPICS='
}

# --- WI_APPLICABLE_DOCS from fixture docs --------------------------------

@test "context: WI_APPLICABLE_DOCS is populated from a matching fixture doc heading" {
  repo_with_config '{"docs_search":{"roots":["docs/plans"],"max":8},"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":[]}}}}}'
  mkdir -p "$GFH_REPO/docs/plans"
  cat >"$GFH_REPO/docs/plans/example.md" <<'DOC'
# Example Plan

## Widget Rollout

Some text about widgets.
DOC
  show_fixture tc-1 '{"id":"tc-1","title":"widget rollout follow-up","labels":[],"metadata":{}}'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_APPLICABLE_DOCS=docs/plans/example.md:Widget Rollout"* ]]
}

@test "context: WI_APPLICABLE_DOCS is empty when nothing matches" {
  repo_with_config '{"docs_search":{"roots":["docs/plans"],"max":8},"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":[]}}}}}'
  mkdir -p "$GFH_REPO/docs/plans"
  cat >"$GFH_REPO/docs/plans/example.md" <<'DOC'
# Example Plan

## Widget Rollout
DOC
  show_fixture tc-1 '{"id":"tc-1","title":"unrelated zzzznope","labels":[],"metadata":{}}'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -qxF 'WI_APPLICABLE_DOCS='
}

# --- stale premise flagged ------------------------------------------------

@test "context: a premise naming a target closed by a newer kind:remove item is flagged stale" {
  repo_with_config '{"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":[]}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"depends on grafana-old","labels":[],"metadata":{"premise":"grafana-old","groomed_at":"2026-01-01T00:00:00Z"}}'
  list_fixture "kind:remove" '[{"id":"tc-r1","title":"remove grafana-old dashboard","status":"closed","closed_at":"2026-02-01T00:00:00Z"}]'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_PREMISE_STALE=true"* ]]
}

@test "context: a premise with no matching closed removal is not flagged stale" {
  repo_with_config '{"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":[]}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"depends on grafana-old","labels":[],"metadata":{"premise":"grafana-old","groomed_at":"2026-01-01T00:00:00Z"}}'
  list_fixture "kind:remove" '[]'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_PREMISE_STALE=false"* ]]
}

@test "context: no premise at all is never flagged stale" {
  repo_with_config '{"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":[]}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"plain item","labels":[],"metadata":{}}'
  run pgwf_context_cmd tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_PREMISE_STALE=false"* ]]
  printf '%s\n' "$output" | grep -qxF 'WI_PREMISE='
}

# --- --render assembles the full prompt ----------------------------------

@test "context --render: static instructions/concerns first, item block last" {
  repo_with_config '{"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"instructions":"<built-in>","concerns":["intent"]}}}}}'
  mkdir -p "$GFH_REPO/.claude/wi-flow/concerns"
  printf 'Is this a duplicate?' >"$GFH_REPO/.claude/wi-flow/concerns/intent.md"
  show_fixture tc-1 '{"id":"tc-1","title":"an item","description":"desc","labels":[],"metadata":{}}'
  run pgwf_context_cmd --render tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"Is this a duplicate?"* ]]
  [[ "$output" == *"## Item"* ]]
  item_pos="${output%%"## Item"*}"
  [[ "$item_pos" == *"Is this a duplicate?"* ]]
}

@test "context --render --role researcher: scoped to id, title, gap, applicable docs only" {
  repo_with_config '{"workflows":{"homelab":{"primary":true,"stages":{"work":{"order":1,"entry":true,"closes":true,"concerns":["intent"]}}}}}'
  show_fixture tc-1 '{"id":"tc-1","title":"an item","description":"secret desc","labels":[],"metadata":{}}'
  run pgwf_context_cmd --render tc-1 --role researcher --gap "which version is current?"
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_ID=tc-1"* ]]
  [[ "$output" == *"which version is current?"* ]]
  [[ "$output" != *"secret desc"* ]]
  [[ "$output" != *"WI_CONCERNS"* ]]
}
