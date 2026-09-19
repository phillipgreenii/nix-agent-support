# shellcheck shell=bash

# pg-wi-flow's two-layer JSON config loader [design: ## Configuration]: a
# machine layer at $XDG_CONFIG_HOME/pg-wi-flow/config.json deep-merged UNDER
# a repo layer at <repo>/.claude/wi-flow/config.json -- repo wins. Maps
# (kinds, topics, workflows, stages, concern_triggers, labels, ...)
# deep-merge by key; arrays are replaced WHOLESALE by the repo layer, never
# concatenated [design: ## Configuration, "Merge rule" paragraph]. jq's `*`
# operator already implements exactly this rule (recursive merge when both
# operands are objects; the right operand wins outright for arrays and
# scalars), so the merge itself is one `jq` call -- this library exists to
# resolve the two file paths, tolerate either layer being absent, and
# resolve the `workflows` map to its effective value, falling back to the
# built-in null workflow when none is configured.
#
# How the machine file is produced is out of scope here: this loader only
# ever reads a file at the resolved path [design: ## Configuration, same
# paragraph].

# The built-in null workflow [design: ## Configuration, the fenced JSON
# block immediately after the "Three terms" paragraph -- quoted
# byte-for-byte]. Applies whenever the merged config has no `workflows` key
# (or an empty one): "do what the bead says, verify as it says, close on its
# acceptance criteria; escalate if amiss."
PGWF_NULL_WORKFLOW_JSON='{
  "work": {
    "order": 1,
    "entry": true,
    "closes": true,
    "instructions": "<built-in>",
    "concerns": [],
    "review_mode": "batched"
  }
}'

# The null workflow has no name of its own in the design -- it applies
# "when workflows defines nothing", not as a named entry alongside
# configured ones. This library still needs a stable key to report it by
# (e.g. as the "workflow" field `next`/`claim` print); "(null)" is reserved
# for that purpose and can never collide with a real config-defined
# workflow name (JSON object keys there are user-chosen labels, and nothing
# stops a name written without going out of its way to include parens, but
# this is the same class of "reserved sentinel" convention used elsewhere
# in this repo, e.g. `--no-labels`).
PGWF_NULL_WORKFLOW_NAME='(null)'

# pgwf_config_machine_path -- the machine layer's default path.
pgwf_config_machine_path() {
  printf '%s/pg-wi-flow/config.json\n' "${XDG_CONFIG_HOME:-$HOME/.config}"
}

# pgwf_config_repo_path [START_DIR] -- the repo layer's default path:
# resolves the git toplevel from START_DIR (default: PWD); falls back to
# START_DIR itself when not inside a git work tree (a bare directory is a
# reasonable "repo root" fallback for ad hoc/non-git use). Explicitly
# unsets every git repository-redirection env var (GIT_DIR, GIT_WORK_TREE,
# GIT_INDEX_FILE, GIT_CEILING_DIRECTORIES, GIT_COMMON_DIR,
# GIT_OBJECT_DIRECTORY) around the `git` call: pg-wi-flow itself may run
# from inside a git hook (or a bats suite driven by one, as observed when
# this repo's own `run-unit-tests` pre-commit hook exercises this function
# -- `git commit`'s own hook execution sets these in the environment),
# where an inherited GIT_DIR would make directory-based .git discovery
# resolve the WRONG repository instead of the one under START_DIR. This
# repo additionally wraps `git` to refuse outright when any of these are
# set and do not resolve under a temporary root (`docs/adr/0059-ceta-temp-
# repo-carve-out.md`) -- clearing them here avoids that refusal too.
pgwf_config_repo_path() {
  local start_dir="${1:-$PWD}" root
  root="$(cd "$start_dir" 2>/dev/null && env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE \
    -u GIT_CEILING_DIRECTORIES -u GIT_COMMON_DIR -u GIT_OBJECT_DIRECTORY \
    git rev-parse --show-toplevel 2>/dev/null)"
  if [[ -z $root ]]; then
    root="$start_dir"
  fi
  printf '%s/.claude/wi-flow/config.json\n' "$root"
}

# pgwf_config_read_layer PATH -- prints PATH's JSON contents (compacted), or
# `{}` when PATH does not exist -- a missing layer is not an error [design:
# ## Configuration, "How the machine file is produced is out of scope"].
# Fails loudly (non-zero, one-line stderr) when PATH exists but is not
# valid JSON, rather than silently treating it as empty.
pgwf_config_read_layer() {
  local path="$1"
  if [[ ! -f $path ]]; then
    printf '{}'
    return 0
  fi
  if ! jq -c '.' "$path" 2>/dev/null; then
    echo "pg-wi-flow: config file is not valid JSON: $path" >&2
    return 1
  fi
}

# pgwf_config_load MACHINE_PATH REPO_PATH -- reads both layers and prints
# the repo-wins deep merge on stdout (compact JSON, one line).
pgwf_config_load() {
  local machine_path="$1" repo_path="$2" machine_json repo_json
  machine_json="$(pgwf_config_read_layer "$machine_path")" || return 1
  repo_json="$(pgwf_config_read_layer "$repo_path")" || return 1
  jq -c -n --argjson m "$machine_json" --argjson r "$repo_json" '$m * $r'
}

# pgwf_config_effective [START_DIR] -- pgwf_config_load against the default
# machine/repo paths (the convenience most CLI verbs want; explicit-path
# pgwf_config_load stays available so callers -- and bats -- can inject
# fixture files directly).
pgwf_config_effective() {
  pgwf_config_load "$(pgwf_config_machine_path)" "$(pgwf_config_repo_path "${1:-}")"
}

# pgwf_config_label CONFIG_JSON KEY DEFAULT -- config.labels.<KEY>, or
# DEFAULT when absent [design: ## Configuration, the "labels" block in the
# fenced JSON example].
pgwf_config_label() {
  local config_json="$1" key="$2" default="$3"
  jq -r --arg k "$key" --arg d "$default" '.labels[$k] // $d' <<<"$config_json"
}

# pgwf_config_exclude_labels CONFIG_JSON -- prints config.exclude_labels,
# one label per line ([] when absent). Applied by every query shape
# [design: ## State model -> Axis 1 -> "Querying", "Every query adds
# exclude_labels"].
pgwf_config_exclude_labels() {
  local config_json="$1"
  jq -r '.exclude_labels[]?' <<<"$config_json"
}

# pgwf_config_iteration_bound CONFIG_JSON -- config.iteration_bound, or 2
# when absent [design: ## Configuration, the fenced JSON example's
# "iteration_bound": 2].
pgwf_config_iteration_bound() {
  local config_json="$1"
  jq -r '.iteration_bound // 2' <<<"$config_json"
}

# pgwf_config_primary_workflow_name CONFIG_JSON -- prints the name of the
# workflow marked "primary": true, or PGWF_NULL_WORKFLOW_NAME when no
# `workflows` key survives the merge [design: ## Configuration, "Three
# terms"]. Fails (non-zero, one-line stderr) when `workflows` IS configured
# but no entry is marked primary -- exactly one MUST be, per the same
# section.
pgwf_config_primary_workflow_name() {
  local config_json="$1" raw_workflows name
  raw_workflows="$(jq -c '.workflows // {}' <<<"$config_json")"
  if [[ $raw_workflows == '{}' ]]; then
    printf '%s\n' "$PGWF_NULL_WORKFLOW_NAME"
    return 0
  fi
  name="$(jq -r 'to_entries | map(select(.value.primary == true)) | .[0].key // empty' <<<"$raw_workflows")"
  if [[ -z $name ]]; then
    echo 'pg-wi-flow: config defines workflows but none is marked "primary": true' >&2
    return 1
  fi
  printf '%s\n' "$name"
}

# pgwf_workflow_stages_by_name CONFIG_JSON NAME -- prints the stage map
# ({stagename: {order, entry, closes, ...}}) for workflow NAME. NAME may be
# PGWF_NULL_WORKFLOW_NAME, in which case the built-in null workflow (shaped
# identically to a configured workflow's own `.stages` map) is printed.
# Prints nothing (empty) when NAME does not exist in CONFIG_JSON.
pgwf_workflow_stages_by_name() {
  local config_json="$1" name="$2"
  if [[ $name == "$PGWF_NULL_WORKFLOW_NAME" ]]; then
    printf '%s' "$PGWF_NULL_WORKFLOW_JSON"
    return 0
  fi
  jq -c --arg n "$name" '.workflows[$n].stages // empty' <<<"$config_json"
}

# pgwf_config_effective_stages CONFIG_JSON -- the stage map of "the"
# workflow query building operates against: the primary configured
# workflow, or the null workflow when none is configured [design: ##
# State model -> Axis 1 -> "Querying" speaks of "the workflow's entry
# stage" without itself taking a workflow argument -- the primary workflow
# is the one driving the backlog at any given time, so query building
# resolves the entry/closing stage against it].
pgwf_config_effective_stages() {
  local config_json="$1" name
  name="$(pgwf_config_primary_workflow_name "$config_json")" || return 1
  pgwf_workflow_stages_by_name "$config_json" "$name"
}

# pgwf_workflow_entry_stage STAGE_MAP_JSON -- the name of the one stage
# carrying "entry": true (C-1: exactly one per workflow), or empty if none
# is marked (a malformed workflow definition).
pgwf_workflow_entry_stage() {
  jq -r 'to_entries | map(select(.value.entry == true)) | .[0].key // empty' <<<"$1"
}

# pgwf_workflow_closing_stage STAGE_MAP_JSON -- the name of the (first)
# stage carrying "closes": true, or empty if none is marked.
pgwf_workflow_closing_stage() {
  jq -r 'to_entries | map(select(.value.closes == true)) | .[0].key // empty' <<<"$1"
}

# pgwf_workflow_stage_names STAGE_MAP_JSON -- every stage name, one per
# line.
pgwf_workflow_stage_names() {
  jq -r 'keys[]' <<<"$1"
}

# pgwf_workflow_stage_exists STAGE_MAP_JSON STAGE -- true (status 0) iff
# STAGE is a key of STAGE_MAP_JSON [design: ## Configuration C-2, "advance
# --to X MUST fail unless X names a stage in the item's workflow"]. Shared
# by `advance` and `create-child --stage` (tc-9ddu3.1.3), which the Contract
# requires validate identically.
pgwf_workflow_stage_exists() {
  jq -e --arg s "$2" 'has($s)' <<<"$1" >/dev/null
}

# pgwf_workflow_stage_order STAGE_MAP_JSON STAGE -- STAGE's `order` value,
# or empty when STAGE is absent or carries no `order` [design: ##
# Configuration C-2, "a move to a stage with a LOWER order... requires
# --reason"].
pgwf_workflow_stage_order() {
  jq -r --arg s "$2" '.[$s].order // empty' <<<"$1"
}
