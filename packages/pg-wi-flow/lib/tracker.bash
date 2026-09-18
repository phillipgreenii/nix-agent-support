# shellcheck shell=bash

# pg-wi-flow's bd Adapter [design: ## Architecture, "Adapter, confined to
# one file"]: the ONLY file in this package that invokes `bd`. Every verb
# this docket adds MUST route its bd calls through here -- no other file
# may shell out to `bd` directly. Exposes, at minimum, a config-aware query
# builder (pgwf_query_build), an item-fetch/update wrapper
# (pgwf_tracker_show_json and friends), and a claim/release wrapper
# (pgwf_tracker_try_claim / pgwf_tracker_release).
#
# Depends on lib/config.bash (composed ahead of this file by mkBashLibrary's
# own `libraries` chaining -- see packages/pg-wi-flow/lib/default.nix).

# pgwf_tracker_show_json ID -- `bd show ID --json`'s .data[0] object
# (compact JSON on stdout). Fails loudly on a `bd` error.
pgwf_tracker_show_json() {
  local id="$1" out
  if ! out="$(bd show "$id" --json 2>&1)"; then
    echo "pg-wi-flow: bd show $id failed: $out" >&2
    return 1
  fi
  jq -c '.data[0]' <<<"$out"
}

# pgwf_tracker_metadata_field ID KEY -- ID's metadata[KEY], or empty if
# absent.
pgwf_tracker_metadata_field() {
  local id="$1" key="$2" item
  item="$(pgwf_tracker_show_json "$id")" || return 1
  jq -r --arg k "$key" '.metadata[$k] // empty' <<<"$item"
}

# pgwf_tracker_parent_id ID -- ID's parent id, or empty if it has none.
pgwf_tracker_parent_id() {
  local id="$1" item
  item="$(pgwf_tracker_show_json "$id")" || return 1
  jq -r '.parent // empty' <<<"$item"
}

# pgwf_tracker_has_label ID LABEL -- true if ID carries LABEL.
pgwf_tracker_has_label() {
  local id="$1" label="$2" item
  item="$(pgwf_tracker_show_json "$id")" || return 1
  jq -e --arg l "$label" '(.labels // []) | index($l) != null' <<<"$item" >/dev/null
}

# pgwf_tracker_item_stage CONFIG_JSON ID -- ID's current stage name (its
# `stage:<x>` label with the configured stage_prefix stripped), or empty
# when ID carries no stage label at all -- which is exactly what an
# entry-stage item looks like (C-1).
pgwf_tracker_item_stage() {
  local config_json="$1" id="$2" prefix item
  prefix="$(pgwf_config_label "$config_json" stage_prefix 'stage:')"
  item="$(pgwf_tracker_show_json "$id")" || return 1
  jq -r --arg p "$prefix" \
    '(.labels // []) | map(select(startswith($p))) | (.[0] // "") | if . == "" then "" else ltrimstr($p) end' \
    <<<"$item"
}

# pgwf_effective_stage_name CONFIG_JSON ID -- ID's concrete stage name:
# its own `stage:<x>` label when it carries one, else the entry stage of
# ID's effective workflow (an item with no stage label at all IS at its
# workflow's entry stage -- C-1 -- it is simply never label-marked there).
# Used both for display ("id stage workflow" lines) and to compose the
# stage-scoped actor a write needs [design: ## State model -> "Identity":
# actor = "$PG_WI_FLOW_IDENT-<stage>", stage read from the item's label "at
# claim time and on every write"].
pgwf_effective_stage_name() {
  local config_json="$1" id="$2" stage workflow stages
  stage="$(pgwf_tracker_item_stage "$config_json" "$id")" || return 1
  if [[ -n $stage ]]; then
    printf '%s\n' "$stage"
    return 0
  fi
  workflow="$(pgwf_workflow_for "$config_json" "$id")" || return 1
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
  pgwf_workflow_entry_stage "$stages"
}

# pgwf_workflow_for CONFIG_JSON ID -- the workflow name effective for ID:
# the `wi_workflow` metadata value of the nearest ancestor (including ID
# itself) that carries one, else the primary workflow, else the built-in
# null workflow [design: ## Configuration, "Three terms"]. Bounded walk
# (64 levels) as a defensive guard against a cyclic/malformed parent chain
# -- bd's own data model does not allow cycles, so this should never bite
# in practice.
pgwf_workflow_for() {
  local config_json="$1" id="$2"
  local cur="$id" wf
  local -i depth=0 max_depth=64
  while [[ -n $cur && $depth -lt $max_depth ]]; do
    wf="$(pgwf_tracker_metadata_field "$cur" wi_workflow)" || return 1
    if [[ -n $wf ]]; then
      printf '%s\n' "$wf"
      return 0
    fi
    cur="$(pgwf_tracker_parent_id "$cur")" || return 1
    depth+=1
  done
  pgwf_config_primary_workflow_name "$config_json"
}

# pgwf_query_build CONFIG_JSON MODE [STAGE...] -- prints, one token per
# line, the `bd ready`/`bd list` filter arguments for the requested query
# shape [design: ## State model -> Axis 1 -> "Querying", quoted verbatim in
# this packet]. MODE is one of:
#   default  -- admits every stage plus escalated questions; excludes only
#               the `human` label (plus exclude_labels, always).
#   attended -- question items labeled human (C-4): `--label <question>
#               --label <human>`.
#   stage    -- one or more --stage values (repeatable). The workflow's
#               entry stage carries no stage label at all (C-1), so it is
#               selected by EXCLUDING every other stage label rather than
#               by a positive --label; every other requested stage is
#               `--label-any stage:<x>` (OR across the given stages).
#
# Freedom boundary: a --stage list that mixes the entry stage with other
# stages cannot be expressed as a single `bd` filter set (entry selection
# is an exclusion, not a positive label) -- not needed in execution phase 1
# (the null workflow has exactly one stage, which is both entry and
# closing). When this happens, the entry-only exclusion form is used and a
# one-line warning is printed to stderr.
pgwf_query_build() {
  local config_json="$1" mode="$2"
  shift 2
  local -a stages=("$@")

  local question_label human_label stage_prefix
  question_label="$(pgwf_config_label "$config_json" question question)"
  human_label="$(pgwf_config_label "$config_json" human human)"
  stage_prefix="$(pgwf_config_label "$config_json" stage_prefix 'stage:')"

  case "$mode" in
  attended)
    printf -- '--label\n%s\n--label\n%s\n' "$question_label" "$human_label"
    ;;
  stage)
    local stages_json entry_stage s has_entry=0
    stages_json="$(pgwf_config_effective_stages "$config_json")" || return 1
    entry_stage="$(pgwf_workflow_entry_stage "$stages_json")"
    for s in "${stages[@]}"; do
      [[ -n $entry_stage && $s == "$entry_stage" ]] && has_entry=1
    done
    if [[ $has_entry -eq 1 ]]; then
      if [[ ${#stages[@]} -gt 1 ]]; then
        echo "pg-wi-flow: query: --stage combining the entry stage ($entry_stage) with other stages is not supported; using the entry-stage exclusion form only" >&2
      fi
      local -a all_stages
      mapfile -t all_stages < <(pgwf_workflow_stage_names "$stages_json")
      for s in "${all_stages[@]}"; do
        [[ $s == "$entry_stage" ]] && continue
        printf -- '--exclude-label\n%s%s\n' "$stage_prefix" "$s"
      done
      printf -- '--exclude-label\n%s\n' "$question_label"
    else
      for s in "${stages[@]}"; do
        printf -- '--label-any\n%s%s\n' "$stage_prefix" "$s"
      done
    fi
    ;;
  default | *)
    printf -- '--exclude-label\n%s\n' "$human_label"
    ;;
  esac

  local e
  while IFS= read -r e; do
    [[ -z $e ]] && continue
    printf -- '--exclude-label\n%s\n' "$e"
  done < <(pgwf_config_exclude_labels "$config_json")
}

# pgwf_tracker_ready ARGS... -- `bd ready ARGS... --json`'s data array
# (compact JSON on stdout), sorted by bd's default priority sort.
pgwf_tracker_ready() {
  local -a args=("$@")
  local out
  if ! out="$(bd ready "${args[@]}" --json 2>&1)"; then
    echo "pg-wi-flow: bd ready failed: $out" >&2
    return 1
  fi
  jq -c '.data // .' <<<"$out"
}

# pgwf_tracker_ready_under PARENT_ID ARGS... -- `bd ready --parent
# PARENT_ID ARGS... --json`'s data array; `--parent` already resolves
# descendants recursively (bd's own filter semantics), which is exactly
# what container descent's case 2 needs [design: ## State model ->
# "Containers", item 2].
pgwf_tracker_ready_under() {
  local parent_id="$1"
  shift
  pgwf_tracker_ready --parent "$parent_id" "$@"
}

# pgwf_tracker_open_children ID -- `bd children ID --json`'s data array,
# filtered to non-closed items (compact JSON array on stdout).
pgwf_tracker_open_children() {
  local id="$1" out
  if ! out="$(bd children "$id" --json 2>&1)"; then
    echo "pg-wi-flow: bd children $id failed: $out" >&2
    return 1
  fi
  jq -c '[(.data // .)[] | select(.status != "closed")]' <<<"$out"
}

# pgwf_tracker_try_claim ID ACTOR -- `bd update ID --claim`, under ACTOR.
# Returns bd's own exit code: 0 on success, non-zero when the claim did not
# happen (already claimed by someone else, unknown id, etc.) -- this is
# exactly the "claim failure" `next` retries against the next candidate on
# [design: ## Configuration C-4].
pgwf_tracker_try_claim() {
  local id="$1" actor="$2"
  bd update "$id" --claim --actor "$actor" --json >/dev/null 2>&1
}

# pgwf_tracker_release ID ACTOR -- releases ID with the assignee cleared in
# the SAME `bd update` call as the status change [design: ## Components,
# "Invariants enforced inside the CLI"; workspace B-1/B-2 restated as a
# CLI-enforced invariant].
pgwf_tracker_release() {
  local id="$1" actor="$2"
  bd update "$id" --status open --assignee "" --actor "$actor" --json
}

# pgwf_advance_stage CONFIG_JSON ID TO_STAGE ACTOR [REASON] -- the internal
# stage-write primitive [design: ## Configuration C-4's closing paragraph;
# this packet's Contract]: swaps ID's stage:<x> label to stage:<TO_STAGE>.
# This packet's `next` calls it for the childless-container case (case 3,
# reason "children complete"); a later packet (P3, row 3) builds the full
# `advance` verb ON TOP of this SAME primitive so `advance` remains the
# only stage-label writer across BOTH packets, not merely within either
# one's own diff. REASON is accepted but not yet recorded anywhere (bd has
# no per-label-change reason field) -- kept as a parameter now so call
# sites do not change shape once P3 lands and gives it somewhere to go
# (e.g. an update `--notes` line).
pgwf_advance_stage() {
  local config_json="$1" id="$2" to_stage="$3" actor="$4"
  # shellcheck disable=SC2034 # reserved for P3, see header comment above
  local _reason="${5:-}"
  local stage_prefix cur_stage
  stage_prefix="$(pgwf_config_label "$config_json" stage_prefix 'stage:')"
  cur_stage="$(pgwf_tracker_item_stage "$config_json" "$id")" || return 1

  local -a remove_args=()
  if [[ -n $cur_stage ]]; then
    remove_args=(--remove-label "${stage_prefix}${cur_stage}")
  fi
  bd update "$id" "${remove_args[@]}" --add-label "${stage_prefix}${to_stage}" --actor "$actor" --json >/dev/null
}
