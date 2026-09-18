#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pg-wi-flow's two-layer JSON config loader (tc-9ddu3.1.1):
# missing-layer tolerance, invalid-JSON refusal, the deep-merge/
# wholesale-array-replace rule, and workflow/stage lookup helpers including
# the built-in null-workflow fallback.
bats_require_minimum_version 1.5.0

# load_git_fixture_harness -- sources the shared hermetic-by-construction
# bats git-fixture harness (pg2-31f13/pg2-gucfd; see
# packages/pg-wi-flow/lib/test-support/git-fixture-harness.bash's own
# header) so a test that needs a real throwaway git repo (gfh_setup /
# gfh_teardown) can get one immune to GIT_DIR-family leakage from an
# enclosing `git commit` hook.
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
  else
    # shellcheck disable=SC1090 # composed lib path is runtime-resolved (nix check)
    source "$LIB_PATH"
  fi

  # lib/default.nix wires three SEPARATE mkBashLibrary derivations (actor,
  # config, tracker) sharing this SAME tests/ directory (mkBashLibrary
  # hardcodes testDir = src + "/tests", and all three share src = ./.).
  # Under actor's own check, LIB_PATH provides only actor.bash's content --
  # config's functions are genuinely absent there, not a bug. Skip
  # gracefully; config's own check (and tracker's, which composes config.bash
  # in) are where this file's tests actually run.
  if ! declare -F pgwf_config_load >/dev/null 2>&1; then
    skip "config.bash functions not present under this library's composed LIB_PATH"
  fi

  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "pgwf_config_read_layer prints {} for a missing file" {
  run pgwf_config_read_layer "$TEST_DIR/does-not-exist.json"
  [ "$status" -eq 0 ]
  [ "$output" = "{}" ]
}

@test "pgwf_config_read_layer passes through valid JSON, compacted" {
  printf '{\n  "a": 1\n}\n' >"$TEST_DIR/config.json"
  run pgwf_config_read_layer "$TEST_DIR/config.json"
  [ "$status" -eq 0 ]
  [ "$output" = '{"a":1}' ]
}

@test "pgwf_config_read_layer refuses invalid JSON, non-zero, one-line stderr" {
  printf 'not json' >"$TEST_DIR/config.json"
  run --separate-stderr pgwf_config_read_layer "$TEST_DIR/config.json"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *"not valid JSON"* ]]
}

@test "pgwf_config_load: both layers absent yields {}" {
  run pgwf_config_load "$TEST_DIR/machine.json" "$TEST_DIR/repo.json"
  [ "$status" -eq 0 ]
  [ "$output" = "{}" ]
}

@test "pgwf_config_load: maps deep-merge by key, repo layer's keys win" {
  cat >"$TEST_DIR/machine.json" <<'EOF'
{ "labels": { "human": "human", "question": "question" }, "iteration_bound": 2 }
EOF
  cat >"$TEST_DIR/repo.json" <<'EOF'
{ "labels": { "human": "human-override" } }
EOF
  run pgwf_config_load "$TEST_DIR/machine.json" "$TEST_DIR/repo.json"
  [ "$status" -eq 0 ]
  result="$(jq -c -S . <<<"$output")"
  [ "$result" = '{"iteration_bound":2,"labels":{"human":"human-override","question":"question"}}' ]
}

@test "pgwf_config_load: arrays are replaced wholesale by the repo layer, never concatenated" {
  cat >"$TEST_DIR/machine.json" <<'EOF'
{ "exclude_labels": ["phase", "critic"] }
EOF
  cat >"$TEST_DIR/repo.json" <<'EOF'
{ "exclude_labels": ["merge-request"] }
EOF
  run pgwf_config_load "$TEST_DIR/machine.json" "$TEST_DIR/repo.json"
  [ "$status" -eq 0 ]
  result="$(jq -c . <<<"$output")"
  [ "$result" = '{"exclude_labels":["merge-request"]}' ]
}

@test "pgwf_config_label: returns the configured value" {
  config='{"labels":{"question":"custom-question"}}'
  run pgwf_config_label "$config" question question
  [ "$status" -eq 0 ]
  [ "$output" = "custom-question" ]
}

@test "pgwf_config_label: falls back to the given default when unset" {
  run pgwf_config_label '{}' stage_prefix 'stage:'
  [ "$status" -eq 0 ]
  [ "$output" = "stage:" ]
}

@test "pgwf_config_exclude_labels: prints each label on its own line" {
  config='{"exclude_labels":["phase","critic","merge-request"]}'
  run pgwf_config_exclude_labels "$config"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "phase" ]
  [ "${lines[1]}" = "critic" ]
  [ "${lines[2]}" = "merge-request" ]
}

@test "pgwf_config_exclude_labels: empty when absent" {
  run pgwf_config_exclude_labels '{}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgwf_config_iteration_bound: configured value" {
  run pgwf_config_iteration_bound '{"iteration_bound": 5}'
  [ "$status" -eq 0 ]
  [ "$output" = "5" ]
}

@test "pgwf_config_iteration_bound: defaults to 2" {
  run pgwf_config_iteration_bound '{}'
  [ "$status" -eq 0 ]
  [ "$output" = "2" ]
}

@test "pgwf_config_primary_workflow_name: no workflows key -> null workflow" {
  run pgwf_config_primary_workflow_name '{}'
  [ "$status" -eq 0 ]
  [ "$output" = "$PGWF_NULL_WORKFLOW_NAME" ]
}

@test "pgwf_config_primary_workflow_name: returns the workflow marked primary" {
  config='{"workflows":{"homelab":{"primary":true,"stages":{}},"other":{"stages":{}}}}'
  run pgwf_config_primary_workflow_name "$config"
  [ "$status" -eq 0 ]
  [ "$output" = "homelab" ]
}

@test "pgwf_config_primary_workflow_name: refuses when workflows configured but none marked primary" {
  config='{"workflows":{"homelab":{"stages":{}}}}'
  run --separate-stderr pgwf_config_primary_workflow_name "$config"
  [ "$status" -ne 0 ]
  [[ "$stderr" == *"none is marked"* ]]
}

@test "pgwf_workflow_stages_by_name: PGWF_NULL_WORKFLOW_NAME prints the built-in null workflow" {
  run pgwf_workflow_stages_by_name '{}' "$PGWF_NULL_WORKFLOW_NAME"
  [ "$status" -eq 0 ]
  result="$(jq -c . <<<"$output")"
  [ "$result" = '{"work":{"order":1,"entry":true,"closes":true,"instructions":"<built-in>","concerns":[],"review_mode":"batched"}}' ]
}

@test "pgwf_workflow_stages_by_name: named workflow returns its stages map" {
  config='{"workflows":{"homelab":{"primary":true,"stages":{"groom":{"order":1,"entry":true}}}}}'
  run pgwf_workflow_stages_by_name "$config" homelab
  [ "$status" -eq 0 ]
  result="$(jq -c . <<<"$output")"
  [ "$result" = '{"groom":{"order":1,"entry":true}}' ]
}

@test "pgwf_config_effective_stages: falls back to the null workflow's stages" {
  run pgwf_config_effective_stages '{}'
  [ "$status" -eq 0 ]
  result="$(jq -r 'keys | .[0]' <<<"$output")"
  [ "$result" = "work" ]
}

@test "pgwf_workflow_entry_stage: finds the entry:true stage" {
  stages='{"groom":{"order":1,"entry":true},"plan":{"order":2}}'
  run pgwf_workflow_entry_stage "$stages"
  [ "$status" -eq 0 ]
  [ "$output" = "groom" ]
}

@test "pgwf_workflow_closing_stage: finds the closes:true stage" {
  stages='{"groom":{"order":1,"entry":true},"learn":{"order":6,"closes":true}}'
  run pgwf_workflow_closing_stage "$stages"
  [ "$status" -eq 0 ]
  [ "$output" = "learn" ]
}

@test "pgwf_workflow_stage_names: lists every stage" {
  stages='{"groom":{},"plan":{},"implement":{}}'
  run pgwf_workflow_stage_names "$stages"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 3 ]
}

@test "pgwf_config_machine_path: honors XDG_CONFIG_HOME" {
  XDG_CONFIG_HOME="$TEST_DIR/xdg" run pgwf_config_machine_path
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/xdg/pg-wi-flow/config.json" ]
}

@test "pgwf_config_repo_path: resolves the git toplevel" {
  # Hermetic-by-construction git fixture (pg2-31f13/pg2-gucfd): a plain
  # `git -C "$TEST_DIR" init` here would be vulnerable to a `git commit`
  # issued from a linked worktree (this repo's own `run-unit-tests`
  # pre-commit hook, among others) leaking GIT_DIR/GIT_CEILING_DIRECTORIES
  # into this test's environment and making rev-parse resolve the REAL
  # repo instead of the throwaway fixture -- observed directly on this
  # test before the harness was adopted here.
  load_git_fixture_harness
  gfh_setup "pg-wi-flow-config"
  mkdir -p "$GFH_REPO/sub/deeper"
  run pgwf_config_repo_path "$GFH_REPO/sub/deeper"
  gfh_teardown
  [ "$status" -eq 0 ]
  [ "$output" = "$GFH_REPO/.claude/wi-flow/config.json" ]
}

@test "pgwf_config_repo_path: falls back to the given directory outside a git tree" {
  load_git_fixture_harness
  gfh_setup "pg-wi-flow-config"
  mkdir -p "$GFH_WORK/not-a-repo"
  run pgwf_config_repo_path "$GFH_WORK/not-a-repo"
  gfh_teardown
  [ "$status" -eq 0 ]
  [ "$output" = "$GFH_WORK/not-a-repo/.claude/wi-flow/config.json" ]
}
