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

# pgwf_cmd_claim ID -- transfers the reservation to the caller's identity
# and prints "id stage workflow" [design: ## Components write-verb table
# row for claim]. Freedom boundary (this packet's own Contract): does NOT
# print the full assembled prompt (context --render's output) -- that
# engine is packet P2 (row 2) in this docket; P2 wires claim to also emit
# it in the same call.
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
