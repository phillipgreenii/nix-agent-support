#!/usr/bin/env bats
# bats file_tags=type:integration
# Regression guard for the plugin-default data (packages/pg-wi-flow/data/):
# a homelab-shaped workflow resolves and renders every shipped stage,
# escalation and concern file (tc-9ddu3.2.10). Runs the real pg-wi-flow CLI
# against a REAL throwaway embedded-Dolt `bd` database created by `bd init`
# inside the hermetic git-fixture-harness repo (same pattern as
# test-pg-wi-flow-integration.bats) -- never the shared remote server, never a
# dolt sql-server. Skips when `bd` is not on PATH (the nix check sandbox).
#
# Data root: the environment variable PG_WI_FLOW_DATA_UNDER_TEST names the
# built pg-wi-flow-data store path. The nix check sets it (the `config` entry
# in pg-wi-flow/default.nix). When it is unset (a direnv/hook run from a
# checkout) the test falls back to the source tree, packages/pg-wi-flow/data,
# which the data derivation copies verbatim.
#
# The fixture workflow mirrors the design's `homelab` example (same stage
# names, orders, entry/closes/skip_kinds, concern lists, review_mode) EXCEPT
# the `verify` stage, whose files are homelab content outside this data root.
# Topic labels are bare (topics carry no prefix; lib/context.bash
# pgwf_context_topics matches labels that are keys of config.topics).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd data-guard tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB PG_WI_FLOW_DATA_UNDER_TEST
  gfh_setup "pg-wi-flow-homelab-data"
  gfh_restore_env

  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="${SCRIPT_UNDER_TEST:-bash $SCRIPTS_DIR/pg-wi-flow.sh}"
  DATA="${PG_WI_FLOW_DATA_UNDER_TEST:-$(cd "$(dirname "$BATS_TEST_FILENAME")/../../data" && pwd)}"
  [[ -d $DATA/stages && -d $DATA/concerns ]] || {
    echo "data root $DATA lacks stages/ or concerns/" >&2
    return 1
  }

  export BEADS_DOLT_AUTO_START=0 BD_NON_INTERACTIVE=1
  XDG_CONFIG_HOME="$GFH_WORK/xdg"
  mkdir -p "$XDG_CONFIG_HOME"
  export XDG_CONFIG_HOME
  cd "$GFH_REPO" || exit 1
  if ! bd init --non-interactive --prefix hd -q >"$GFH_WORK/init.log" 2>&1; then
    cat "$GFH_WORK/init.log" >&2
    skip "bd init could not create a throwaway embedded database"
  fi
  [[ "$(jq -r '.dolt_mode // empty' .beads/metadata.json)" == embedded ]] ||
    skip "throwaway bd database is not embedded; refusing to run"

  write_fixture_config
  export PG_WI_FLOW_IDENT="aaaaaaaa-main-main"
}

teardown() {
  if declare -F gfh_teardown >/dev/null; then
    gfh_teardown
  fi
  return 0
}

# The design's homelab config example minus the verify stage; paths.defaults
# is the data root under test.
write_fixture_config() {
  mkdir -p "$GFH_REPO/.claude/wi-flow"
  jq -n --arg data "$DATA" '{
    workflows: { homelab: { primary: true, stages: {
      groom: { order: 1, entry: true, instructions: "stages/groom.md",
        escalation: "stages/groom/escalation.md",
        concerns: ["intent", "conformance", "secondary-effects", "destructive-actions"],
        review_mode: "batched" },
      plan: { order: 2, instructions: "stages/plan.md",
        escalation: "stages/plan/escalation.md",
        concerns: ["sufficiency"], review_mode: "parallel" },
      decompose: { order: 3, instructions: "stages/decompose.md",
        escalation: "stages/decompose/escalation.md",
        concerns: ["decomposition", "conformance", "secondary-effects", "destructive-actions"],
        review_mode: "parallel" },
      implement: { order: 4, instructions: "stages/implement.md",
        escalation: "stages/implement/escalation.md", concerns: [] },
      learn: { order: 6, instructions: "stages/learn.md",
        escalation: "stages/learn/escalation.md", concerns: [],
        closes: true, skip_kinds: ["land"] }
    } } },
    labels: { stage_prefix: "stage:", kind_prefix: "kind:", component_prefix: "component:",
      question: "question", escalated: "escalated", human: "human",
      container: "container", trigger_prefix: "q:" },
    exclude_labels: ["phase", "critic", "merge-request"],
    kinds: { upgrade: "bump a dependency or app version", bug: "something is broken",
      config_change: "change existing configuration", land: "lands a worktree'"'"'s implementation items" },
    topics: { "touches-auth": "changes authentication or authorization",
      "touches-secrets": "creates, rotates or reads a secret" },
    concern_triggers: { "touches-auth": ["security-review"],
      "touches-secrets": ["secrets-handling"] },
    iteration_bound: 2,
    paths: { defaults: $data, repo_local: ".claude/wi-flow" }
  }' >"$GFH_REPO/.claude/wi-flow/config.json"
}

# make_item TITLE LABEL... -- prints the new id
make_item() {
  local title="$1" label args=()
  shift
  for label in "$@"; do args+=(--labels "$label"); done
  bd create "$title" -t task "${args[@]}" --json | jq -r '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end | .id'
}

# Config-order concern names of a stage in the fixture.
stage_concerns() {
  jq -r --arg s "$1" '.workflows.homelab.stages[$s].concerns[]' "$GFH_REPO/.claude/wi-flow/config.json"
}

# Section of a render: the text between "## Concerns" and "## Item".
concerns_section() {
  local rest="${1#*"## Concerns"}"
  printf '%s' "${rest%%"## Item"*}"
}

# (1) every shipped stage renders its instruction text and its concern texts,
# in config order.
@test "every stage except verify renders its instructions and its concern texts in config order" {
  local stage id text concern section rest n
  for stage in groom plan decompose implement learn; do
    if [[ $stage == groom ]]; then
      id="$(make_item "item at $stage")"
    else
      id="$(make_item "item at $stage" "stage:$stage")"
    fi
    run --separate-stderr $SCRIPT context --render "$id"
    [ "$status" -eq 0 ] || {
      echo "stage $stage: context --render exited $status: $stderr" >&2
      return 1
    }
    [[ $stderr != *"not found"* ]] || {
      echo "stage $stage: unresolved data file: $stderr" >&2
      return 1
    }
    # instruction text (the whole shipped file) is present
    text="$(<"$DATA/stages/$stage.md")"
    [ -n "$text" ]
    [[ $output == *"$text"* ]] || {
      echo "stage $stage: instructions from $DATA/stages/$stage.md not in render" >&2
      return 1
    }
    # every concern text is present, in config order
    n=0
    section="$(concerns_section "$output")"
    rest="$section"
    while IFS= read -r concern; do
      [ -n "$concern" ] || continue
      n=$((n + 1))
      text="$(<"$DATA/concerns/$concern.md")"
      [ -n "$text" ]
      [[ $rest == *"### $concern"* ]] || {
        echo "stage $stage: concern $concern missing or out of order" >&2
        return 1
      }
      rest="${rest#*"### $concern"}"
      [[ $rest == *"$text"* ]] || {
        echo "stage $stage: text of concern $concern missing" >&2
        return 1
      }
    done < <(stage_concerns "$stage")
    if [ "$n" -eq 0 ]; then
      [[ $output != *"## Concerns"* ]]
    fi
  done
}

# (2) repo overlay overrides a stage file and wins a concern collision; the
# other default concerns remain (union).
@test "repo-overlay stage file overrides the default; overlay concern wins its collision and defaults remain" {
  mkdir -p "$GFH_REPO/.claude/wi-flow/stages" "$GFH_REPO/.claude/wi-flow/concerns"
  printf 'OVERLAY-PLAN-INSTRUCTIONS-SENTINEL\n' >"$GFH_REPO/.claude/wi-flow/stages/plan.md"
  printf 'OVERLAY-INTENT-SENTINEL\n' >"$GFH_REPO/.claude/wi-flow/concerns/intent.md"

  local plan groom
  plan="$(make_item "plan item" stage:plan)"
  run --separate-stderr $SCRIPT context --render "$plan"
  [ "$status" -eq 0 ]
  [[ $output == *OVERLAY-PLAN-INSTRUCTIONS-SENTINEL* ]]
  [[ $output != *"$(<"$DATA/stages/plan.md")"* ]]

  groom="$(make_item "groom item")"
  run --separate-stderr $SCRIPT context --render "$groom"
  [ "$status" -eq 0 ]
  [[ $output == *OVERLAY-INTENT-SENTINEL* ]]
  [[ $output != *"$(<"$DATA/concerns/intent.md")"* ]]
  # union: the non-overridden default concerns still render
  [[ $output == *"$(<"$DATA/concerns/conformance.md")"* ]]
  [[ $output == *"$(<"$DATA/concerns/secondary-effects.md")"* ]]
  [[ $output == *"$(<"$DATA/concerns/destructive-actions.md")"* ]]
}

# (3) a topic whose triggered concern file ships nowhere is a warning, not an
# error (rule C-5). security-review and secrets-handling are not in the data.
@test "a topic label whose triggered concern file does not resolve warns on stderr and exits 0" {
  [ ! -e "$DATA/concerns/security-review.md" ]
  local id
  id="$(make_item "auth item" touches-auth)"
  run --separate-stderr $SCRIPT context --render "$id"
  [ "$status" -eq 0 ]
  [[ $stderr == *"concern file not found, skipping: security-review"* ]]
  # the stage's own concerns still rendered
  [[ $output == *"### intent"* ]]
  [[ $output != *"### security-review"* ]]
}

# (4) static prefix byte-identical across two items of the same stage+kind.
@test "the static prompt prefix is byte-identical across two items of the same stage and kind" {
  mkdir -p "$GFH_REPO/.claude/wi-flow/checklists"
  printf 'CHECKLIST-FOR-BUG-SENTINEL\n' >"$GFH_REPO/.claude/wi-flow/checklists/bug.md"
  local a b out_a out_b prefix_a prefix_b
  a="$(make_item "first bug alpha" stage:decompose kind:bug)"
  b="$(make_item "second bug zeta quux" stage:decompose kind:bug)"
  run --separate-stderr $SCRIPT context --render "$a"
  [ "$status" -eq 0 ]
  out_a="$output"
  run --separate-stderr $SCRIPT context --render "$b"
  [ "$status" -eq 0 ]
  out_b="$output"
  [[ $out_a != "$out_b" ]]
  [[ $out_a == *"## Item"* ]]
  prefix_a="${out_a%%"## Item"*}"
  prefix_b="${out_b%%"## Item"*}"
  [ -n "$prefix_a" ]
  [ "$prefix_a" = "$prefix_b" ]
  # the static block really carries all four static parts
  [[ $prefix_a == *"## Instructions"* ]]
  [[ $prefix_a == *CHECKLIST-FOR-BUG-SENTINEL* ]]
  [[ $prefix_a == *"### decomposition"* ]]
  [[ $prefix_a == *"$(<"$DATA/stages/decompose.md")"* ]]
  # item-specific text comes last: titles appear only after the prefix
  [[ $prefix_a != *"first bug alpha"* ]]
  [[ ${out_a#"$prefix_a"} == *"first bug alpha"* ]]
}

# (5) WI_LESSONS reads memories recorded under the P2 key convention
# (lesson-<component>-<slug>).
@test "WI_LESSONS is non-empty for an item whose component has a recorded memory" {
  bd remember "do not retry the helm upgrade without pruning orphans" --key lesson-longhorn-helm-orphans >/dev/null
  bd remember "unrelated component lesson" --key lesson-other-thing >/dev/null
  local with without
  with="$(make_item "longhorn item" component:longhorn)"
  without="$(make_item "other-less item" component:nomemory)"

  run --separate-stderr $SCRIPT context "$with"
  [ "$status" -eq 0 ]
  [[ $output == *"WI_LESSONS="*"do not retry the helm upgrade without pruning orphans"* ]]
  [[ $output != *"unrelated component lesson"* ]]

  run --separate-stderr $SCRIPT context "$without"
  [ "$status" -eq 0 ]
  [[ $output == *"WI_LESSONS="* ]]
  [[ $output != *"do not retry the helm upgrade"* ]]
  [ -z "$(grep '^WI_LESSONS=' <<<"$output" | cut -d= -f2-)" ]
}

# (6) resolve --decision on a destructive-actions question with no
# authorizing doc files the kind:docs follow-up (tc-h4334, design gap G-D).
# Real bd: the resolve round trip exercises the recorded originating_concern
# metadata, the blocks-edge lookup and create-child. The question is built
# directly with bd (labels, metadata, blocks edge) rather than via
# `pg-wi-flow escalate`, because escalate creates it with --parent and bd
# 1.3.1 then refuses the parent-blocked-by-child edge (reported separately).
make_destructive_question() {
  local parent="$1" qid
  qid="$(make_item "may we drop the scratch table?" question escalated q:info)"
  bd update "$qid" --set-metadata originating_concern=destructive-actions >/dev/null
  bd dep add "$parent" "$qid" >/dev/null
  printf '%s' "$qid"
}

kind_docs_children() {
  bd list --label kind:docs --json --limit 0 |
    jq -c --arg p "$1" '(if type=="object" and has("data") then .data else . end) | map(select(.id | startswith($p + ".")))'
}

@test "resolve --decision on a destructive-actions question files a kind:docs follow-up under the blocked parent" {
  local parent qid docs_json
  parent="$(make_item "parent needing a destructive action")"
  qid="$(make_destructive_question "$parent")"

  run --separate-stderr $SCRIPT resolve "$qid" --decision "yes, drop it" --rationale "scratch data"
  [ "$status" -eq 0 ]
  [ "$(bd show "$qid" --json | jq -r '(if type=="array" then .[0] else . end).status')" = closed ]

  docs_json="$(kind_docs_children "$parent")"
  [ "$(jq 'length' <<<"$docs_json")" -eq 1 ]
  [[ "$(jq -r '.[0].title' <<<"$docs_json")" == "Write the authorizing doc for the destructive action approved in $qid"* ]]
}

@test "resolve --decision --authorizing-doc on a destructive-actions question files no kind:docs follow-up" {
  local parent qid
  parent="$(make_item "parent with an authorized destructive action")"
  qid="$(make_destructive_question "$parent")"

  run --separate-stderr $SCRIPT resolve "$qid" --decision "yes" --rationale "runbook covers it" --authorizing-doc docs/runbooks/prune.md
  [ "$status" -eq 0 ]
  [ "$(kind_docs_children "$parent" | jq 'length')" -eq 0 ]
}
