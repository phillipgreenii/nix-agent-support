# shellcheck shell=bash

# nix build already sources lib/{actor,config,tracker}.bash ahead of this
# file (mkBashScript's `libraries` list) and this file ahead of
# pg-wi-flow.sh (hasSupportBash injection); this guard only fires for a raw
# `bash pg-wi-flow.sh`/`source pg-wi-flow.bash` run (e.g. local bats, or an
# interactive checkout run) where nothing has sourced the external
# libraries yet.
if ! declare -F pgwf_query_build >/dev/null 2>&1; then
  __pgwf_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)"
  # shellcheck disable=SC1091 # sibling lib dir, resolved at source time
  source "$__pgwf_lib_dir/config.bash"
  # shellcheck disable=SC1091 # sibling lib dir, resolved at source time
  source "$__pgwf_lib_dir/tracker.bash"
  # shellcheck disable=SC1091 # sibling lib dir, resolved at source time
  source "$__pgwf_lib_dir/actor.bash"
  # shellcheck disable=SC1091 # sibling lib dir, resolved at source time
  source "$__pgwf_lib_dir/context.bash"
  unset __pgwf_lib_dir
fi

# PGWF_EXPLICIT_ACTOR -- populated by pg-wi-flow.sh's global --actor
# parsing, ONLY accepted outside Claude Code [design: ## Architecture,
# "Command pattern"; ## State model -> "Identity"]. Empty inside Claude
# Code, where the actor always composes from $PG_WI_FLOW_IDENT-<stage>.
: "${PGWF_EXPLICIT_ACTOR:=}"

# pgwf_current_actor STAGE -- composes the actor for a write verb via
# lib/actor.bash's pgwf_compose_actor [design: ## State model ->
# "Identity"]. Never call `bd` (via tracker.bash) without routing the actor
# through this.
pgwf_current_actor() {
  local stage="$1"
  pgwf_compose_actor "$PGWF_EXPLICIT_ACTOR" "$stage"
}

# pgwf_next_resolve_target CONFIG_JSON ID [QUERY_ARGS...] -- container
# descent, all four cases [design: ## State model -> "Containers", quoted
# verbatim in this packet]. Prints the resolved target id on success, or
# nothing (empty output, status 0) for case 4 ("skip, `next` continues").
# QUERY_ARGS are the same filter args the caller's overall `next` query was
# built with, applied to descent so a container's descendant still has to
# match the caller's --stage restriction. Composes its own stage-scoped
# actor per write (case 3's advance) from the ITEM being written, per
# "Identity": stage is read from the item's own label/entry-stage state at
# write time, not passed in from the caller.
pgwf_next_resolve_target() {
  local config_json="$1" id="$2"
  shift 2
  local -a query_args=("$@")

  if ! pgwf_tracker_has_label "$id" container; then
    printf '%s\n' "$id" # case 1: leaf
    return 0
  fi

  local open_children
  open_children="$(pgwf_tracker_open_children "$id")" || return 1

  if [[ $(jq 'length' <<<"$open_children") -eq 0 ]]; then
    # case 3: childless container -- the container itself is the work item.
    # Advance it to its workflow's closing stage through the SAME code
    # path `advance` uses (reason "children complete"), THEN dispatch it.
    local workflow stages closing stage actor
    workflow="$(pgwf_workflow_for "$config_json" "$id")" || return 1
    stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
    closing="$(pgwf_workflow_closing_stage "$stages")"
    if [[ -n $closing ]]; then
      stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
      actor="$(pgwf_current_actor "$stage")" || return 1
      pgwf_advance_stage "$config_json" "$id" "$closing" "$actor" "children complete" || return 1
    fi
    printf '%s\n' "$id"
    return 0
  fi

  # case 2: take the highest-ranked ready descendant instead (recursively
  # -- `--parent` already resolves descendants at any depth).
  local ready_json ready_id
  ready_json="$(pgwf_tracker_ready_under "$id" "${query_args[@]}")" || return 1
  ready_id="$(jq -r '.[0].id // empty' <<<"$ready_json")"
  if [[ -z $ready_id ]]; then
    return 0 # case 4: open children, all unready -> skip
  fi
  pgwf_next_resolve_target "$config_json" "$ready_id" "${query_args[@]}"
}

# pgwf_cmd_query [--stage s]... [--attended] -- prints the fully built
# filter set (one bd flag/value per line) [design: ## Components, read-verb
# table row for query].
pgwf_cmd_query() {
  local -a stage_args=()
  local attended=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
    --stage)
      stage_args+=("$2")
      shift 2
      ;;
    --attended)
      attended=1
      shift
      ;;
    *)
      echo "pg-wi-flow: query: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done

  local config_json
  config_json="$(pgwf_config_effective)" || return 1

  if [[ $attended -eq 1 ]]; then
    pgwf_query_build "$config_json" attended
  elif [[ ${#stage_args[@]} -gt 0 ]]; then
    pgwf_query_build "$config_json" stage "${stage_args[@]}"
  else
    pgwf_query_build "$config_json" default
  fi
}

# pgwf_cmd_list [--stage s]... [--attended] [--questions] [--unpooled]
# [--stale --days N | --stale --reserved-hours H] -- items the query would
# admit [design: ## Components, read-verb table row for list, quoted in
# full in this packet]. Prints a compact JSON array on stdout.
pgwf_cmd_list() {
  local -a stage_args=()
  local attended=0 unpooled=0 stale=0 days="" hours=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
    --stage)
      stage_args+=("$2")
      shift 2
      ;;
    --attended)
      attended=1
      shift
      ;;
    --questions)
      attended=1
      shift
      ;;
    --unpooled)
      unpooled=1
      shift
      ;;
    --stale)
      stale=1
      shift
      ;;
    --days)
      days="$2"
      shift 2
      ;;
    --reserved-hours)
      hours="$2"
      shift 2
      ;;
    *)
      echo "pg-wi-flow: list: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done

  local config_json
  config_json="$(pgwf_config_effective)" || return 1

  if [[ $stale -eq 1 ]]; then
    if [[ -n $days && -n $hours ]]; then
      echo "pg-wi-flow: list: --stale takes --days OR --reserved-hours, not both" >&2
      return 1
    fi
    if [[ -n $hours ]]; then
      pgwf_list_stale_reserved "$config_json" "$hours"
    else
      pgwf_list_stale_days "$config_json" "${days:-14}"
    fi
    return $?
  fi

  if [[ $unpooled -eq 1 ]]; then
    pgwf_list_unpooled "$config_json"
    return $?
  fi

  local -a query_args
  if [[ $attended -eq 1 ]]; then
    mapfile -t query_args < <(pgwf_query_build "$config_json" attended)
  elif [[ ${#stage_args[@]} -gt 0 ]]; then
    mapfile -t query_args < <(pgwf_query_build "$config_json" stage "${stage_args[@]}")
  else
    mapfile -t query_args < <(pgwf_query_build "$config_json" default)
  fi
  pgwf_tracker_ready "${query_args[@]}"
}

# pgwf_list_stale_days CONFIG_JSON DAYS -- questions and legacy human items
# (INCLUDING status blocked) older than DAYS days [design: ## Components,
# list's --stale --days row].
pgwf_list_stale_days() {
  local config_json="$1" days="$2" question_label human_label out cutoff
  question_label="$(pgwf_config_label "$config_json" question question)"
  human_label="$(pgwf_config_label "$config_json" human human)"
  cutoff="$(date -u -d "-${days} days" +%Y-%m-%dT%H:%M:%SZ)"
  if ! out="$(pgwf_tracker_list --label-any "$question_label" --label-any "$human_label" \
    --status open,in_progress,blocked,deferred)"; then
    echo "pg-wi-flow: list --stale --days failed" >&2
    return 1
  fi
  jq -c --arg cutoff "$cutoff" '[.[] | select(.updated_at < $cutoff)]' <<<"$out"
}

# pgwf_list_stale_reserved CONFIG_JSON HOURS -- reservations whose holder
# role is `dispatcher`, older than HOURS hours [design: ## Components,
# list's --stale --reserved-hours row]. The holder role is read from the
# assignee string's "-dispatcher-" segment (see ## State model ->
# "Identity": actor = "$PG_WI_FLOW_IDENT-<stage>" and PG_WI_FLOW_IDENT
# already embeds the agent_type -- "dispatcher" for a dispatcher -- as its
# own middle segment, so a literal "-dispatcher-" substring match is a
# reliable, if approximate, way to spot one without depending on any
# particular id-hyphenation shape upstream of it).
pgwf_list_stale_reserved() {
  local config_json="$1" hours="$2" out cutoff
  cutoff="$(date -u -d "-${hours} hours" +%Y-%m-%dT%H:%M:%SZ)"
  if ! out="$(pgwf_tracker_list --status in_progress)"; then
    echo "pg-wi-flow: list --stale --reserved-hours failed" >&2
    return 1
  fi
  jq -c --arg cutoff "$cutoff" \
    '[.[] | select((.assignee // "") | test("-dispatcher-")) | select(.updated_at < $cutoff)]' \
    <<<"$out"
}

# pgwf_list_unpooled CONFIG_JSON -- open items no query admits [design: ##
# Components, list's --unpooled row]: open/in_progress items that neither
# the default (broadest positive-selection) query nor the attention query
# would return.
pgwf_list_unpooled() {
  local config_json="$1" all default_ready attended_ready
  local -a default_args attended_args
  mapfile -t default_args < <(pgwf_query_build "$config_json" default)
  mapfile -t attended_args < <(pgwf_query_build "$config_json" attended)
  default_ready="$(pgwf_tracker_ready "${default_args[@]}")" || return 1
  attended_ready="$(pgwf_tracker_ready "${attended_args[@]}")" || return 1

  if ! all="$(pgwf_tracker_list --status open,in_progress)"; then
    echo "pg-wi-flow: list --unpooled failed" >&2
    return 1
  fi
  jq -c --argjson d "$default_ready" --argjson a "$attended_ready" \
    '($d + $a | map(.id)) as $admitted | [.[] | select((.id as $i | $admitted | index($i)) == null)]' \
    <<<"$all"
}

# pgwf_cmd_next [--stage s]... -- reads the ready set, computes the target
# (leaf or container descent), claims it, prints "id stage workflow" or
# "none" [design: ## Configuration C-4; ## Components write-verb table row
# for next].
pgwf_cmd_next() {
  local -a stage_args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --stage)
      stage_args+=("$2")
      shift 2
      ;;
    *)
      echo "pg-wi-flow: next: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done

  local config_json
  config_json="$(pgwf_config_effective)" || return 1

  local -a query_args
  if [[ ${#stage_args[@]} -gt 0 ]]; then
    mapfile -t query_args < <(pgwf_query_build "$config_json" stage "${stage_args[@]}")
  else
    mapfile -t query_args < <(pgwf_query_build "$config_json" default)
  fi

  local iteration_bound
  iteration_bound="$(pgwf_config_iteration_bound "$config_json")"

  local candidates_json
  candidates_json="$(pgwf_tracker_ready "${query_args[@]}")" || return 1

  local -i n
  n="$(jq 'length' <<<"$candidates_json")"
  if [[ $n -eq 0 ]]; then
    echo none
    return 0
  fi

  local -i i=0 attempts=0
  while [[ $i -lt $n && $attempts -lt $iteration_bound ]]; do
    local cand_id
    cand_id="$(jq -r --argjson i "$i" '.[$i].id' <<<"$candidates_json")"
    i+=1

    local target
    target="$(pgwf_next_resolve_target "$config_json" "$cand_id" "${query_args[@]}")" || return 1
    if [[ -z $target ]]; then
      continue # case 4: skip, does not consume an attempt
    fi

    local target_stage claim_actor
    target_stage="$(pgwf_effective_stage_name "$config_json" "$target")" || return 1
    claim_actor="$(pgwf_current_actor "$target_stage")" || return 1

    attempts+=1
    if pgwf_tracker_try_claim "$target" "$claim_actor"; then
      local workflow
      workflow="$(pgwf_workflow_for "$config_json" "$target")" || return 1
      printf '%s %s %s\n' "$target" "$target_stage" "$workflow"
      return 0
    fi
    # lost race: fall through to the next candidate
  done

  echo none
}

# pgwf_cmd_claim ID -- transfers the reservation to the caller's identity,
# prints "id stage workflow", THEN the full assembled prompt (context
# --render's output) in the SAME call [design: ## Components write-verb
# table row for claim; ## Components -> Worker step 1: "claim <id>
# transfers the reservation ... and prints the full assembled prompt in
# the same call"]. Closes the gap packet P1 (tc-9ddu3.1.1) deliberately
# left open; the id/stage/workflow line stays first and unchanged so
# P1's documented "id stage workflow" contract (which the dispatcher/
# worker agents parse, packet P6) still recovers cleanly from the combined
# output.
pgwf_cmd_claim() {
  local id="$1"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: claim: missing ID" >&2
    return 1
  fi

  local config_json stage actor
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1

  if ! pgwf_tracker_try_claim "$id" "$actor"; then
    echo "pg-wi-flow: claim: failed to claim $id" >&2
    return 1
  fi

  local workflow
  workflow="$(pgwf_workflow_for "$config_json" "$id")" || return 1
  printf '%s %s %s\n' "$id" "$stage" "$workflow"
  pgwf_context_cmd --render "$id"
}

# pgwf_cmd_release ID -- releases with the assignee cleared in ONE call
# [design: ## Components write-verb table row for release].
pgwf_cmd_release() {
  local id="$1"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: release: missing ID" >&2
    return 1
  fi

  local config_json stage actor
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1

  pgwf_tracker_release "$id" "$actor" >/dev/null
}

# pgwf_stamp_top_level_workflow CONFIG_JSON ID ACTOR -- stamps ID's
# `wi_workflow` metadata with the primary configured workflow's name
# [design: ## Configuration, "Three terms": "every top-level item the CLI
# creates is stamped with it [the primary workflow]"]. A no-op when no real
# workflow is configured (the primary resolves to the built-in null
# workflow): stamping the null sentinel would permanently pin the item to
# it, defeating the ancestry-fallback mechanism a later phase's real
# `workflows` config relies on. Used by close --trace's `filed:` items --
# the only NEW top-level (parentless) items this packet's verbs create.
pgwf_stamp_top_level_workflow() {
  local config_json="$1" id="$2" actor="$3" primary
  primary="$(pgwf_config_primary_workflow_name "$config_json")" || return 1
  if [[ $primary != "$PGWF_NULL_WORKFLOW_NAME" ]]; then
    pgwf_tracker_update "$id" "$actor" --set-metadata "wi_workflow=${primary}" >/dev/null
  fi
}

# pgwf_prefixed_labels_to_remove ITEM_JSON PREFIX -- ITEM_JSON's existing
# PREFIX-prefixed labels (kind:/component:, both single-valued per ##
# State model -> Axis 3), one per line -- what annotate's --kind/
# --component must --remove-label before --add-label'ing the new value.
pgwf_prefixed_labels_to_remove() {
  local item_json="$1" prefix="$2"
  jq -r --arg p "$prefix" '(.labels // [])[] | select(startswith($p))' <<<"$item_json"
}

# pgwf_cmd_annotate ID [--kind k] [--component c] [--premise p]
# [--acceptance t] [--append-description t] [--append-notes t]
# [--design t] -- the only way to write item content; flags are combinable
# in one call [design: ## Components table, annotate row]. --kind/
# --component replace any existing same-prefixed label (single-valued, ##
# State model Axis 3); --premise is stored in metadata (WI_PREMISE has no
# native bd field); --acceptance/--design/--append-notes route straight to
# bd's own flags; --append-description reads the current description and
# appends with a newline separator (bd has no native --append-description).
pgwf_cmd_annotate() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: annotate: missing ID" >&2
    return 1
  fi
  shift

  local kind="" component="" premise="" acceptance="" append_description="" append_notes="" design=""
  local have_kind=0 have_component=0 have_premise=0 have_acceptance=0
  local have_append_description=0 have_append_notes=0 have_design=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --kind)
      kind="$2"
      have_kind=1
      shift 2
      ;;
    --component)
      component="$2"
      have_component=1
      shift 2
      ;;
    --premise)
      premise="$2"
      have_premise=1
      shift 2
      ;;
    --acceptance)
      acceptance="$2"
      have_acceptance=1
      shift 2
      ;;
    --append-description)
      append_description="$2"
      have_append_description=1
      shift 2
      ;;
    --append-notes)
      append_notes="$2"
      have_append_notes=1
      shift 2
      ;;
    --design)
      design="$2"
      have_design=1
      shift 2
      ;;
    *)
      echo "pg-wi-flow: annotate: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done

  if [[ $have_kind -eq 0 && $have_component -eq 0 && $have_premise -eq 0 && $have_acceptance -eq 0 &&
    $have_append_description -eq 0 && $have_append_notes -eq 0 && $have_design -eq 0 ]]; then
    echo "pg-wi-flow: annotate: at least one flag is required" >&2
    return 1
  fi

  local config_json stage actor item_json
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1
  item_json="$(pgwf_tracker_show_json "$id")" || return 1

  local -a update_args=()

  if [[ $have_kind -eq 1 ]]; then
    local kind_prefix l
    kind_prefix="$(pgwf_config_label "$config_json" kind_prefix 'kind:')"
    while IFS= read -r l; do
      [[ -n $l ]] && update_args+=(--remove-label "$l")
    done < <(pgwf_prefixed_labels_to_remove "$item_json" "$kind_prefix")
    update_args+=(--add-label "${kind_prefix}${kind}")
  fi

  if [[ $have_component -eq 1 ]]; then
    local component_prefix l
    component_prefix="$(pgwf_config_label "$config_json" component_prefix 'component:')"
    while IFS= read -r l; do
      [[ -n $l ]] && update_args+=(--remove-label "$l")
    done < <(pgwf_prefixed_labels_to_remove "$item_json" "$component_prefix")
    update_args+=(--add-label "${component_prefix}${component}")
  fi

  [[ $have_premise -eq 1 ]] && update_args+=(--set-metadata "wi_premise=${premise}")
  [[ $have_acceptance -eq 1 ]] && update_args+=(--acceptance "$acceptance")
  [[ $have_design -eq 1 ]] && update_args+=(--design "$design")
  [[ $have_append_notes -eq 1 ]] && update_args+=(--append-notes "$append_notes")

  if [[ $have_append_description -eq 1 ]]; then
    local cur_desc new_desc
    cur_desc="$(jq -r '.description // ""' <<<"$item_json")"
    if [[ -n $cur_desc ]]; then
      new_desc="${cur_desc}"$'\n'"${append_description}"
    else
      new_desc="$append_description"
    fi
    update_args+=(--description "$new_desc")
  fi

  pgwf_tracker_update "$id" "$actor" "${update_args[@]}" >/dev/null
}

# pgwf_cmd_record_verdict ID --concern C --json <file|-> -- run BY THE
# REVIEWER LEAF; records C's verdict for the CURRENT round in item metadata
# [design: ## Components table, record-verdict row; ## Reviewer verdict
# contract for the illustrative --json shape]. `round` reads it back.
# Concrete schema (this packet's own freedom boundary, documented per the
# packet's Binding decisions): the raw --json payload is stored verbatim,
# compacted, under metadata key "wi_verdict_r<round>_<concern>" -- round
# re-parses it directly (bd metadata values are plain strings throughout
# this codebase, e.g. wi_workflow; no double-JSON-encoding is introduced).
pgwf_cmd_record_verdict() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: record-verdict: missing ID" >&2
    return 1
  fi
  shift

  local concern="" json_src=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --concern)
      concern="$2"
      shift 2
      ;;
    --json)
      json_src="$2"
      shift 2
      ;;
    *)
      echo "pg-wi-flow: record-verdict: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done
  if [[ -z $concern || -z $json_src ]]; then
    echo "pg-wi-flow: record-verdict: --concern and --json are required" >&2
    return 1
  fi

  local payload
  if [[ $json_src == "-" ]]; then
    payload="$(cat)"
  else
    if [[ ! -f $json_src ]]; then
      echo "pg-wi-flow: record-verdict: no such file: $json_src" >&2
      return 1
    fi
    payload="$(cat "$json_src")"
  fi

  local compact
  if ! compact="$(jq -c '.' <<<"$payload" 2>/dev/null)"; then
    echo "pg-wi-flow: record-verdict: --json is not valid JSON" >&2
    return 1
  fi

  local verdict
  verdict="$(jq -r '.verdict // empty' <<<"$compact")"
  case "$verdict" in
  ready | gaps | blocked | duplicate | related) ;;
  *)
    echo "pg-wi-flow: record-verdict: verdict must be one of ready|gaps|blocked|duplicate|related, got: ${verdict:-<none>}" >&2
    return 1
    ;;
  esac

  local config_json stage actor round
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1
  round="$(pgwf_tracker_metadata_field "$id" wi_round)" || return 1
  round="${round:-0}"

  pgwf_tracker_update "$id" "$actor" --set-metadata "wi_verdict_r${round}_${concern}=${compact}" >/dev/null
}

# pgwf_cmd_round ID -- reads every verdict record-verdict recorded for the
# CURRENT round from item metadata (no --verdicts file -- nothing is passed
# in by hand); merges them, increments the round counter, and prints the
# merged verdict plus WI_MUST_ESCALATE when the (post-increment) counter
# reaches iteration_bound without ready [design: ## The flow -> "Per-stage
# loop" steps 1-2, quoted verbatim in this packet's own Contract]. Merge
# priority (this packet's own freedom boundary): any concern verdict
# `blocked` wins outright; else the `intent` concern's own
# duplicate/related verdict wins (per "duplicate <id> / related <ids> when
# the intent verdict says so"); else any `gaps` wins; else `ready`.
pgwf_cmd_round() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: round: missing ID" >&2
    return 1
  fi

  local config_json stage actor item_json round iteration_bound
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1
  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  round="$(jq -r '.metadata.wi_round // "0"' <<<"$item_json")"
  iteration_bound="$(pgwf_config_iteration_bound "$config_json")"

  local prefix="wi_verdict_r${round}_"
  local -a verdicts=()
  mapfile -t verdicts < <(jq -r --arg p "$prefix" \
    '(.metadata // {}) | to_entries[] | select(.key | startswith($p)) | .value' <<<"$item_json")

  if [[ ${#verdicts[@]} -eq 0 ]]; then
    echo "pg-wi-flow: round: no verdicts recorded for round $round" >&2
    return 1
  fi

  local blocked=0 any_gaps=0 intent_verdict="" intent_of=""
  local v verdict_value concern_value
  for v in "${verdicts[@]}"; do
    verdict_value="$(jq -r '.verdict // empty' <<<"$v")"
    concern_value="$(jq -r '.concern // empty' <<<"$v")"
    case "$verdict_value" in
    blocked) blocked=1 ;;
    gaps) any_gaps=1 ;;
    esac
    if [[ $concern_value == intent ]]; then
      intent_verdict="$verdict_value"
      intent_of="$(jq -r '(.of // []) | join(",")' <<<"$v")"
    fi
  done

  local merged
  if [[ $blocked -eq 1 ]]; then
    merged="blocked"
  elif [[ $intent_verdict == duplicate ]]; then
    merged="duplicate ${intent_of}"
  elif [[ $intent_verdict == related ]]; then
    merged="related ${intent_of}"
  elif [[ $any_gaps -eq 1 ]]; then
    merged="gaps"
  else
    merged="ready"
  fi

  local -i new_round=$((round + 1))
  pgwf_tracker_update "$id" "$actor" --set-metadata "wi_round=${new_round}" >/dev/null

  printf '%s\n' "$merged"
  if [[ $merged != "ready" && $new_round -ge $iteration_bound ]]; then
    echo "WI_MUST_ESCALATE=true"
  fi
}

# pgwf_cmd_advance ID --to STAGE [--reason TEXT] -- validates C-2 (STAGE
# exists in ID's workflow; a move to a LOWER-order stage requires --reason),
# swaps the stage label through pgwf_advance_stage (the ONE stage-label
# writer, shared with `next`'s childless-container case and create-child's
# --stage), and adds `container` if ID has any children [design: ##
# Configuration C-2; ## Components table, advance row; ## State model ->
# Axis 1, "advance is the ONLY writer of stage labels"]. NEVER creates a
# land bead.
pgwf_cmd_advance() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: advance: missing ID" >&2
    return 1
  fi
  shift

  local to="" reason=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --to)
      to="$2"
      shift 2
      ;;
    --reason)
      reason="$2"
      shift 2
      ;;
    *)
      echo "pg-wi-flow: advance: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done
  if [[ -z $to ]]; then
    echo "pg-wi-flow: advance: --to is required" >&2
    return 1
  fi

  local config_json workflow stages
  config_json="$(pgwf_config_effective)" || return 1
  workflow="$(pgwf_workflow_for "$config_json" "$id")" || return 1
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"

  if ! pgwf_workflow_stage_exists "$stages" "$to"; then
    echo "pg-wi-flow: advance: $to is not a stage in workflow $workflow" >&2
    return 1
  fi

  local cur_stage cur_order to_order actor
  cur_stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  cur_order="$(pgwf_workflow_stage_order "$stages" "$cur_stage")"
  to_order="$(pgwf_workflow_stage_order "$stages" "$to")"

  if [[ -n $cur_order && -n $to_order ]] && ((to_order < cur_order)) && [[ -z $reason ]]; then
    echo "pg-wi-flow: advance: moving to a lower-order stage ($to) requires --reason" >&2
    return 1
  fi

  actor="$(pgwf_current_actor "$cur_stage")" || return 1
  pgwf_advance_stage "$config_json" "$id" "$to" "$actor" "$reason" || return 1

  local children
  children="$(pgwf_tracker_any_children "$id")" || return 1
  if [[ "$(jq 'length' <<<"$children")" -gt 0 ]] && ! pgwf_tracker_has_label "$id" container; then
    pgwf_tracker_update "$id" "$actor" --add-label container >/dev/null
  fi
}

# pgwf_cmd_create_child PARENT --title T [--kind K] [--stage S]
# [--blocked-by ID]... [--description T] -- new child at the workflow's
# entry stage (or --stage, validated against the child's INHERITED
# workflow exactly like C-2); parent gains `container` in the same call and
# STAYS OPEN; no inherited labels, no supersedes link; each --blocked-by
# adds a blocking edge from the child to ID (repeatable) [design: ##
# Components table, create-child row; ## State model -> "Containers"].
# --stage's label write (when non-entry) goes through the SAME
# pgwf_advance_stage primitive `advance` uses, keeping "advance is the only
# stage-label writer" true even here.
pgwf_cmd_create_child() {
  local parent="${1:-}"
  if [[ -z $parent ]]; then
    echo "pg-wi-flow: create-child: missing PARENT" >&2
    return 1
  fi
  shift

  local title="" kind="" stage="" description=""
  local -a blocked_by=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --title)
      title="$2"
      shift 2
      ;;
    --kind)
      kind="$2"
      shift 2
      ;;
    --stage)
      stage="$2"
      shift 2
      ;;
    --blocked-by)
      blocked_by+=("$2")
      shift 2
      ;;
    --description)
      description="$2"
      shift 2
      ;;
    *)
      echo "pg-wi-flow: create-child: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done
  if [[ -z $title ]]; then
    echo "pg-wi-flow: create-child: --title is required" >&2
    return 1
  fi

  local config_json workflow stages entry_stage target_stage
  config_json="$(pgwf_config_effective)" || return 1
  workflow="$(pgwf_workflow_for "$config_json" "$parent")" || return 1
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
  entry_stage="$(pgwf_workflow_entry_stage "$stages")"

  if [[ -n $stage ]]; then
    if ! pgwf_workflow_stage_exists "$stages" "$stage"; then
      echo "pg-wi-flow: create-child: $stage is not a stage in workflow $workflow" >&2
      return 1
    fi
    target_stage="$stage"
  else
    target_stage="$entry_stage"
  fi

  local parent_stage actor
  parent_stage="$(pgwf_effective_stage_name "$config_json" "$parent")" || return 1
  actor="$(pgwf_current_actor "$parent_stage")" || return 1

  local -a create_args=(--title "$title" --parent "$parent" --no-inherit-labels)
  [[ -n $description ]] && create_args+=(--description "$description")
  if [[ -n $kind ]]; then
    local kind_prefix
    kind_prefix="$(pgwf_config_label "$config_json" kind_prefix 'kind:')"
    create_args+=(--labels "${kind_prefix}${kind}")
  fi

  local created child_id
  created="$(pgwf_tracker_create "$actor" "${create_args[@]}")" || return 1
  child_id="$(jq -r '.id // empty' <<<"$created")"
  if [[ -z $child_id ]]; then
    echo "pg-wi-flow: create-child: bd create did not return an id" >&2
    return 1
  fi

  if [[ -n $target_stage && $target_stage != "$entry_stage" ]]; then
    pgwf_advance_stage "$config_json" "$child_id" "$target_stage" "$actor" || return 1
  fi

  if ! pgwf_tracker_has_label "$parent" container; then
    pgwf_tracker_update "$parent" "$actor" --add-label container >/dev/null
  fi

  local b
  for b in "${blocked_by[@]}"; do
    pgwf_tracker_add_dependency "$child_id" "$b" "$actor" >/dev/null || return 1
  done

  printf '%s %s %s\n' "$child_id" "$target_stage" "$workflow"
}

# pgwf_cmd_merge ID... --into SURVIVOR -- closes each listed ID as duplicate
# of SURVIVOR with related links; SURVIVOR gets a note listing the merged
# symptoms [design: ## Components table, merge row].
pgwf_cmd_merge() {
  local -a ids=()
  local survivor=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --into)
      survivor="$2"
      shift 2
      ;;
    *)
      ids+=("$1")
      shift
      ;;
    esac
  done
  if [[ -z $survivor ]]; then
    echo "pg-wi-flow: merge: --into is required" >&2
    return 1
  fi
  if [[ ${#ids[@]} -eq 0 ]]; then
    echo "pg-wi-flow: merge: at least one ID to merge is required" >&2
    return 1
  fi

  local config_json survivor_stage actor
  config_json="$(pgwf_config_effective)" || return 1
  survivor_stage="$(pgwf_effective_stage_name "$config_json" "$survivor")" || return 1
  actor="$(pgwf_current_actor "$survivor_stage")" || return 1

  local -a symptoms=()
  local id item_json t
  for id in "${ids[@]}"; do
    item_json="$(pgwf_tracker_show_json "$id")" || return 1
    t="$(jq -r '.title // ""' <<<"$item_json")"
    symptoms+=("$id ($t)")
    pgwf_tracker_duplicate "$id" "$survivor" "$actor" >/dev/null || return 1
    pgwf_tracker_relate "$id" "$survivor" "$actor" >/dev/null || return 1
  done

  local note_text joined
  joined="$(
    local IFS=', '
    echo "${symptoms[*]}"
  )"
  note_text="Merged from: ${joined}"
  pgwf_tracker_note "$survivor" "$note_text" "$actor" >/dev/null
}

# pgwf_close_trace CONFIG_JSON ID ACTOR ENTRY... -- close --trace's
# mechanics [design: ## The flow -> "Pointer path", quoted verbatim in this
# packet's Contract]: parses every "<bullet>=<disposition>" ENTRY, refuses
# the close unless every bullet parsed from ID's own description has one,
# adds a `related` link per traced-id disposition, and files a new
# entry-stage item (ID's own priority) for every "filed:<title>"
# disposition, related-linked back to ID. A disposition that is neither an
# existing id nor "filed:..." is a plain label -- recorded implicitly by
# having been supplied, no further CLI action.
pgwf_close_trace() {
  local config_json="$1" id="$2" actor="$3"
  shift 3
  local -a entries=("$@")

  local item_json description
  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  description="$(jq -r '.description // ""' <<<"$item_json")"

  local -a bullets=()
  while IFS= read -r line; do
    [[ -n $line ]] && bullets+=("$line")
  done < <(grep -E '^[[:space:]]*[-*][[:space:]]+' <<<"$description" |
    sed -E 's/^[[:space:]]*[-*][[:space:]]+//; s/[[:space:]]+$//')

  if [[ ${#bullets[@]} -eq 0 ]]; then
    echo "pg-wi-flow: close --trace: $id has no parsed bullets to trace" >&2
    return 1
  fi

  local -A dispositions=()
  local entry bullet disp
  for entry in "${entries[@]}"; do
    bullet="${entry%%=*}"
    disp="${entry#*=}"
    dispositions["$bullet"]="$disp"
  done

  local -a missing=()
  for bullet in "${bullets[@]}"; do
    if [[ -z ${dispositions[$bullet]+x} ]]; then
      missing+=("$bullet")
    fi
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "pg-wi-flow: close --trace: missing disposition for: ${missing[*]}" >&2
    return 1
  fi

  local priority
  priority="$(jq -r '.priority // empty' <<<"$item_json")"

  for bullet in "${bullets[@]}"; do
    disp="${dispositions[$bullet]}"
    case "$disp" in
    filed:*)
      local new_title="${disp#filed:}"
      local -a create_args=(--title "$new_title" --no-inherit-labels)
      [[ -n $priority ]] && create_args+=(--priority "$priority")
      local created new_id
      created="$(pgwf_tracker_create "$actor" "${create_args[@]}")" || return 1
      new_id="$(jq -r '.id // empty' <<<"$created")"
      if [[ -z $new_id ]]; then
        echo "pg-wi-flow: close --trace: bd create did not return an id for bullet: $bullet" >&2
        return 1
      fi
      pgwf_stamp_top_level_workflow "$config_json" "$new_id" "$actor" || return 1
      pgwf_tracker_relate "$id" "$new_id" "$actor" >/dev/null || return 1
      ;;
    *)
      if pgwf_tracker_show_json "$disp" >/dev/null 2>&1; then
        pgwf_tracker_relate "$id" "$disp" "$actor" >/dev/null || return 1
      fi
      ;;
    esac
  done
}

# pgwf_cmd_close ID --reason TEXT [--trace "bullet=disposition"]... --
# closes; with --trace, refuses unless every parsed bullet of a pointer
# item has a disposition, links traced ids, files filed: items at the
# workflow's entry stage with the pointer's priority [design: ## Components
# table, close row; ## The flow -> "Pointer path"].
pgwf_cmd_close() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: close: missing ID" >&2
    return 1
  fi
  shift

  local reason=""
  local -a trace_entries=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --reason)
      reason="$2"
      shift 2
      ;;
    --trace)
      trace_entries+=("$2")
      shift 2
      ;;
    *)
      echo "pg-wi-flow: close: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done
  if [[ -z $reason ]]; then
    echo "pg-wi-flow: close: --reason is required" >&2
    return 1
  fi

  local config_json stage actor
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1

  if [[ ${#trace_entries[@]} -gt 0 ]]; then
    pgwf_close_trace "$config_json" "$id" "$actor" "${trace_entries[@]}" || return 1
  fi

  pgwf_tracker_close "$id" "$reason" "$actor" >/dev/null
}

# pgwf_cmd_close_duplicate ID --of OF -- refuses unless ID is NEWER than OF
# and OF is open on a FRESH read (so two concurrent sessions cannot close
# each other's item); adds a `related` link [design: ## Components table,
# close-duplicate row -- the survivor rule, MANDATED verbatim per this
# packet's Binding decisions].
pgwf_cmd_close_duplicate() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: close-duplicate: missing ID" >&2
    return 1
  fi
  shift

  local of=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --of)
      of="$2"
      shift 2
      ;;
    *)
      echo "pg-wi-flow: close-duplicate: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done
  if [[ -z $of ]]; then
    echo "pg-wi-flow: close-duplicate: --of is required" >&2
    return 1
  fi

  local id_json of_json id_created of_created of_status
  id_json="$(pgwf_tracker_show_json "$id")" || return 1
  of_json="$(pgwf_tracker_show_json "$of")" || return 1
  id_created="$(jq -r '.created_at // empty' <<<"$id_json")"
  of_created="$(jq -r '.created_at // empty' <<<"$of_json")"
  of_status="$(jq -r '.status // empty' <<<"$of_json")"

  if [[ -z $id_created || -z $of_created ]]; then
    echo "pg-wi-flow: close-duplicate: could not determine created_at for $id or $of" >&2
    return 1
  fi
  if [[ ! ($id_created > $of_created) ]]; then
    echo "pg-wi-flow: close-duplicate: refused: $id ($id_created) is not newer than $of ($of_created)" >&2
    return 1
  fi
  if [[ $of_status == closed ]]; then
    echo "pg-wi-flow: close-duplicate: refused: $of is already closed" >&2
    return 1
  fi

  local config_json stage actor
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1

  pgwf_tracker_duplicate "$id" "$of" "$actor" >/dev/null || return 1
  pgwf_tracker_relate "$id" "$of" "$actor" >/dev/null || return 1
}

# pgwf_escalation_normalize_question TEXT -- the "normalized question text"
# fingerprint dedupe hashes [design: ## State model -> Axis 2, "[rev7]
# Fingerprint dedupe"]. Freedom boundary (this packet's own Contract):
# case-folds and collapses/trims whitespace so cosmetic phrasing
# differences (extra spaces, a trailing newline, case) don't defeat reuse.
# Any future caller composing the SAME fingerprint (execution-phase-2 stage
# workers) MUST normalize identically to this.
pgwf_escalation_normalize_question() {
  local text="$1"
  printf '%s' "$text" | tr '[:upper:]' '[:lower:]' | tr -s '[:space:]' ' ' | sed -e 's/^ //' -e 's/ $//'
}

# pgwf_escalation_fingerprint TRIGGER QUESTION -- fingerprint =
# sha256(trigger + normalized question text), verbatim [design: ## State
# model -> Axis 2, "[rev7] Fingerprint dedupe"]. Freedom boundary (this
# packet's own Contract): sha256sum (coreutils), with a `shasum` fallback
# for macOS -- same precedent as packages/claude-activity/lib/claude-
# activity-lib.bash's get_session_id.
pgwf_escalation_fingerprint() {
  local trigger="$1" question="$2" normalized combined
  normalized="$(pgwf_escalation_normalize_question "$question")"
  combined="${trigger}${normalized}"
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$combined" | sha256sum | cut -d' ' -f1
  else
    printf '%s' "$combined" | shasum -a 256 | cut -d' ' -f1
  fi
}

# pgwf_escalation_valid_trigger TRIGGER -- true iff TRIGGER is one of the
# fixed trigger vocabulary [design: ## State model -> Axis 2, opening
# paragraph: "q:intent | q:info | q:conflict | q:stall"].
pgwf_escalation_valid_trigger() {
  case "$1" in
  q:intent | q:info | q:conflict | q:stall) return 0 ;;
  *) return 1 ;;
  esac
}

# pgwf_resolve_actor_role -- the caller's "role" for resolve --decision's
# q:intent refusal [design: ## State model -> Axis 2, "[rev7] resolve
# --decision MUST refuse..."; ## Identity, "role = agent_type"]:
# PG_WI_FLOW_IDENT's THIRD hyphen-separated field (agent_type), pinned by
# pg-wi-flow-identity.bash's pgwfi_compose_ident to always be exactly the
# last field, so stripping up to the last "-" is exact, not a heuristic.
# Outside Claude Code (no PG_WI_FLOW_IDENT, an explicit --actor override)
# there is no agent framework and so no agent_type to refuse on -- treated
# as the equivalent of the interactive main session.
pgwf_resolve_actor_role() {
  if [[ -z ${PG_WI_FLOW_IDENT:-} ]]; then
    printf 'main\n'
    return 0
  fi
  printf '%s\n' "${PG_WI_FLOW_IDENT##*-}"
}

# pgwf_cmd_escalate ID [--question T --trigger t]... -- two modes [design:
# ## Components table, escalate row; ## State model -> Axis 2]:
#
# 1. One or more --question/--trigger pairs (zipped positionally by order
#    of appearance): ID is the BLOCKED work item. Per pair, computes the
#    fingerprint, reuses an OPEN question already carrying it (adds a
#    blocking edge from ID to that question, creates nothing) or creates a
#    new question child (bd type task, --no-inherit-labels, labeled
#    question+escalated+the trigger, `fingerprint` metadata set), then adds
#    the blocking edge from ID to it. The check-then-create race is
#    ACCEPTED, not prevented [rev7] -- no locking is added here.
# 2. No --question given at all: ID must already carry the question label
#    -- the resolver, unable to settle it, bumps escalated -> human [design:
#    Components table escalate row, "on a question: escalated -> human";
#    the escalation-ladder diagram's "resolver -> operator" hop]. No new
#    question, no fingerprint work.
pgwf_cmd_escalate() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: escalate: missing ID" >&2
    return 1
  fi
  shift

  local -a questions=() triggers=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --question)
      questions+=("$2")
      shift 2
      ;;
    --trigger)
      triggers+=("$2")
      shift 2
      ;;
    *)
      echo "pg-wi-flow: escalate: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done

  local config_json
  config_json="$(pgwf_config_effective)" || return 1

  if [[ ${#questions[@]} -eq 0 ]]; then
    if ! pgwf_tracker_has_label "$id" question; then
      echo "pg-wi-flow: escalate: --question is required (or ID must already be a question, to bump escalated -> human)" >&2
      return 1
    fi
    local stage actor human_label escalated_label
    stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
    actor="$(pgwf_current_actor "$stage")" || return 1
    human_label="$(pgwf_config_label "$config_json" human human)"
    escalated_label="$(pgwf_config_label "$config_json" escalated escalated)"
    local -a bump_args=(--add-label "$human_label")
    if pgwf_tracker_has_label "$id" "$escalated_label"; then
      bump_args+=(--remove-label "$escalated_label")
    fi
    pgwf_tracker_update "$id" "$actor" "${bump_args[@]}" >/dev/null
    return $?
  fi

  if [[ ${#questions[@]} -ne ${#triggers[@]} ]]; then
    echo "pg-wi-flow: escalate: --question and --trigger must be given in matching pairs" >&2
    return 1
  fi

  local i trigger question
  for ((i = 0; i < ${#questions[@]}; i++)); do
    trigger="${triggers[$i]}"
    if ! pgwf_escalation_valid_trigger "$trigger"; then
      echo "pg-wi-flow: escalate: --trigger must be one of q:intent|q:info|q:conflict|q:stall, got: $trigger" >&2
      return 1
    fi
  done

  local stage actor question_label escalated_label
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1
  question_label="$(pgwf_config_label "$config_json" question question)"
  escalated_label="$(pgwf_config_label "$config_json" escalated escalated)"

  for ((i = 0; i < ${#questions[@]}; i++)); do
    trigger="${triggers[$i]}"
    question="${questions[$i]}"

    local fp existing existing_id
    fp="$(pgwf_escalation_fingerprint "$trigger" "$question")"
    existing="$(pgwf_tracker_list --metadata-field "fingerprint=$fp" --label "$question_label" \
      --status open,in_progress,blocked,deferred)" || return 1
    existing_id="$(jq -r '.[0].id // empty' <<<"$existing")"

    if [[ -n $existing_id ]]; then
      pgwf_tracker_add_dependency "$id" "$existing_id" "$actor" >/dev/null || return 1
      continue
    fi

    local -a create_args=(
      --title "$question"
      --labels "${question_label},${escalated_label},${trigger}"
      --no-inherit-labels
      --metadata "$(jq -cn --arg fp "$fp" '{fingerprint: $fp}')"
    )
    local created new_id
    created="$(pgwf_tracker_create "$actor" "${create_args[@]}")" || return 1
    new_id="$(jq -r '.id // empty' <<<"$created")"
    if [[ -z $new_id ]]; then
      echo "pg-wi-flow: escalate: bd create did not return an id" >&2
      return 1
    fi
    pgwf_tracker_add_dependency "$id" "$new_id" "$actor" >/dev/null || return 1
  done
}

# pgwf_cmd_resolve ID (--decision D --rationale R | --answer A | --abandon
# --reason-code r | --defer d) -- the four fixed outcomes, verbatim
# [design: ## State model -> Axis 2, resolve outcomes paragraph;
# "[rev7] resolve --decision MUST refuse..."; "Legacy human items"
# paragraph]. Exactly one outcome per call.
pgwf_cmd_resolve() {
  local id="${1:-}"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: resolve: missing ID" >&2
    return 1
  fi
  shift

  local decision="" rationale="" answer="" reason_code="" defer_date=""
  local have_decision=0 have_rationale=0 have_answer=0 have_abandon=0 have_reason_code=0 have_defer=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --decision)
      decision="$2"
      have_decision=1
      shift 2
      ;;
    --rationale)
      rationale="$2"
      have_rationale=1
      shift 2
      ;;
    --answer)
      answer="$2"
      have_answer=1
      shift 2
      ;;
    --abandon)
      have_abandon=1
      shift
      ;;
    --reason-code)
      reason_code="$2"
      have_reason_code=1
      shift 2
      ;;
    --defer)
      defer_date="$2"
      have_defer=1
      shift 2
      ;;
    *)
      echo "pg-wi-flow: resolve: unknown argument: $1" >&2
      return 1
      ;;
    esac
  done

  local -i outcome_count=0
  [[ $have_decision -eq 1 || $have_rationale -eq 1 ]] && outcome_count+=1
  [[ $have_answer -eq 1 ]] && outcome_count+=1
  [[ $have_abandon -eq 1 || $have_reason_code -eq 1 ]] && outcome_count+=1
  [[ $have_defer -eq 1 ]] && outcome_count+=1
  if [[ $outcome_count -ne 1 ]]; then
    echo "pg-wi-flow: resolve: exactly one of --decision/--rationale, --answer, --abandon/--reason-code, --defer is required" >&2
    return 1
  fi

  if [[ $have_decision -eq 1 || $have_rationale -eq 1 ]] && [[ $have_decision -ne 1 || $have_rationale -ne 1 ]]; then
    echo "pg-wi-flow: resolve: --decision requires --rationale (and vice versa)" >&2
    return 1
  fi

  if [[ $have_abandon -eq 1 || $have_reason_code -eq 1 ]]; then
    if [[ $have_abandon -ne 1 || $have_reason_code -ne 1 ]]; then
      echo "pg-wi-flow: resolve: --abandon requires --reason-code (and vice versa)" >&2
      return 1
    fi
    case "$reason_code" in
    moot-premise | superseded | wont-do | duplicate) ;;
    *)
      echo "pg-wi-flow: resolve: --reason-code must be one of moot-premise|superseded|wont-do|duplicate, got: $reason_code" >&2
      return 1
      ;;
    esac
  fi

  local config_json stage actor item_json
  config_json="$(pgwf_config_effective)" || return 1
  stage="$(pgwf_effective_stage_name "$config_json" "$id")" || return 1
  actor="$(pgwf_current_actor "$stage")" || return 1
  item_json="$(pgwf_tracker_show_json "$id")" || return 1

  local question_label human_label escalated_label
  question_label="$(pgwf_config_label "$config_json" question question)"
  human_label="$(pgwf_config_label "$config_json" human human)"
  escalated_label="$(pgwf_config_label "$config_json" escalated escalated)"

  local is_question=0 is_legacy_human=0
  if jq -e --arg l "$question_label" '(.labels // []) | index($l) != null' <<<"$item_json" >/dev/null; then
    is_question=1
  elif jq -e --arg l "$human_label" '(.labels // []) | index($l) != null' <<<"$item_json" >/dev/null; then
    is_legacy_human=1
  else
    echo "pg-wi-flow: resolve: $id is neither a question nor a legacy human item" >&2
    return 1
  fi

  if [[ $is_question -eq 1 && $have_decision -eq 1 ]]; then
    if jq -e '(.labels // []) | index("q:intent") != null' <<<"$item_json" >/dev/null; then
      local role
      role="$(pgwf_resolve_actor_role)"
      if [[ $role != main ]]; then
        echo "pg-wi-flow: resolve: --decision refused for a q:intent question unless the actor's role is main (got: ${role:-<unknown>})" >&2
        return 1
      fi
    fi
  fi

  if [[ $is_legacy_human -eq 1 ]]; then
    if [[ $have_answer -eq 1 ]]; then
      pgwf_tracker_update "$id" "$actor" --append-notes "$answer" --remove-label "$human_label" >/dev/null || return 1
      pgwf_tracker_release "$id" "$actor" >/dev/null
      return $?
    elif [[ $have_abandon -eq 1 ]]; then
      pgwf_tracker_close "$id" "resolve --abandon --reason-code $reason_code" "$actor" >/dev/null
      return $?
    elif [[ $have_defer -eq 1 ]]; then
      pgwf_tracker_update "$id" "$actor" --defer "$defer_date" >/dev/null
      return $?
    else
      echo "pg-wi-flow: resolve: legacy human items only accept --answer, --abandon --reason-code, or --defer" >&2
      return 1
    fi
  fi

  # Question items: clear whichever attention label is present [design: ##
  # Components, "Invariants enforced inside the CLI": "attention labels
  # only through escalate/resolve"].
  local -a clear_args=()
  if jq -e --arg l "$escalated_label" '(.labels // []) | index($l) != null' <<<"$item_json" >/dev/null; then
    clear_args+=(--remove-label "$escalated_label")
  fi
  if jq -e --arg l "$human_label" '(.labels // []) | index($l) != null' <<<"$item_json" >/dev/null; then
    clear_args+=(--remove-label "$human_label")
  fi
  if [[ ${#clear_args[@]} -gt 0 ]]; then
    pgwf_tracker_update "$id" "$actor" "${clear_args[@]}" >/dev/null || return 1
  fi

  if [[ $have_defer -eq 1 ]]; then
    pgwf_tracker_update "$id" "$actor" --defer "$defer_date" >/dev/null
    return $?
  fi

  if [[ $have_decision -eq 1 ]]; then
    pgwf_tracker_update "$id" "$actor" --append-notes "decision: ${decision}; rationale: ${rationale}" >/dev/null || return 1
    pgwf_tracker_close "$id" "$decision" "$actor" >/dev/null
    return $?
  fi

  if [[ $have_answer -eq 1 ]]; then
    pgwf_tracker_update "$id" "$actor" --append-notes "answer: ${answer}" >/dev/null || return 1
    pgwf_tracker_close "$id" "$answer" "$actor" >/dev/null
    return $?
  fi

  # --abandon: fetch parents BEFORE closing (the dependency edge survives
  # the question's own close either way, but this keeps the read ordering
  # obviously correct). moot-premise additionally files a groom-stage
  # follow-up [design: same section]; ONLY --abandon closes a parent for
  # which this was the last open blocker -- --decision/--answer just
  # "resume" the parent through the ordinary dependency rule.
  local parents
  parents="$(pgwf_tracker_blocks "$id")" || return 1

  pgwf_tracker_close "$id" "resolve --abandon --reason-code $reason_code" "$actor" >/dev/null || return 1

  if [[ $reason_code == moot-premise ]]; then
    local first_parent
    first_parent="$(jq -r '.[0].id // empty' <<<"$parents")"
    if [[ -n $first_parent ]]; then
      pgwf_cmd_create_child "$first_parent" \
        --title "Clean up remnants: $id abandoned as moot-premise" >/dev/null || return 1
    fi
  fi

  local parent_id other_open
  while IFS= read -r parent_id; do
    [[ -z $parent_id ]] && continue
    other_open="$(pgwf_tracker_open_blockers_excluding "$parent_id" "$id")" || return 1
    if [[ "$(jq 'length' <<<"$other_open")" -eq 0 ]]; then
      pgwf_tracker_close "$parent_id" "last open blocker abandoned ($id)" "$actor" >/dev/null || return 1
    fi
  done < <(jq -r '.[].id' <<<"$parents")
}
