# shellcheck shell=bash

# pg-wi-flow's render engine (tc-9ddu3.1.2, Build-plan row 2) [design: ##
# Components -> the read-verb table row for `context`; ## Components ->
# "Rendered prompt contract"]. Builds the classification/render facts for an
# item (kind/topics/instructions/checklist/concerns/duplicates/docs/premise/
# siblings/questions/workflow) and assembles either the plain WI_* line dump
# (`context <id>`) or the full prompt (`context --render <id>`).
#
# Depends on lib/config.bash and lib/tracker.bash (composed ahead of this
# file by mkBashLibrary's own `libraries` chaining -- see
# packages/pg-wi-flow/lib/default.nix). This file itself never calls `bd`
# directly -- every fact is read through lib/tracker.bash's adapter
# functions, extending that adapter (pgwf_tracker_children,
# pgwf_tracker_dependents) rather than shelling out here [design: ##
# Architecture, "Adapter, confined to one file"].

# --- small local helpers -----------------------------------------------

# pgwf_context_repo_root [DIR] -- the git toplevel of DIR (default: PWD), or
# DIR itself when not inside a git work tree. Mirrors
# lib/config.bash's pgwf_config_repo_path git-resolution logic (same
# GIT_*-unset rationale -- see that function's header comment) but without
# the trailing `.claude/wi-flow/config.json` suffix, since this file
# resolves DATA overlay paths (`stages/`, `concerns/`, `checklists/`), not
# the config file itself.
pgwf_context_repo_root() {
  local dir="${1:-$PWD}" root
  root="$(cd "$dir" 2>/dev/null && env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE \
    -u GIT_CEILING_DIRECTORIES -u GIT_COMMON_DIR -u GIT_OBJECT_DIRECTORY \
    git rev-parse --show-toplevel 2>/dev/null)"
  if [[ -z $root ]]; then
    root="$dir"
  fi
  printf '%s\n' "$root"
}

# pgwf_context_data_file CONFIG_JSON REL -- resolves REL (e.g.
# "stages/groom.md", "concerns/intent.md", "checklists/bug.md") against the
# data overlay: repo overlay first (<repo>/<paths.repo_local>/REL), else
# plugin defaults (<paths.defaults>/REL) [design: C-3]. Prints the resolved
# file's contents; returns 1 (nothing printed) when neither resolves.
pgwf_context_data_file() {
  local config_json="$1" rel="$2" repo_local defaults repo_root candidate
  repo_local="$(jq -r '.paths.repo_local // ".claude/wi-flow"' <<<"$config_json")"
  defaults="$(jq -r '.paths.defaults // empty' <<<"$config_json")"
  repo_root="$(pgwf_context_repo_root)"
  candidate="$repo_root/$repo_local/$rel"
  if [[ -f $candidate ]]; then
    cat "$candidate"
    return 0
  fi
  if [[ -n $defaults ]]; then
    candidate="$defaults/$rel"
    if [[ -f $candidate ]]; then
      cat "$candidate"
      return 0
    fi
  fi
  return 1
}

# pgwf_context_has_label ITEM_JSON LABEL -- true if ITEM_JSON's own labels
# array carries LABEL (an item-json-taking sibling of
# lib/tracker.bash's pgwf_tracker_has_label, which re-fetches by id -- this
# file already holds the item JSON for the item under render, so it reuses
# that instead of an extra `bd show`).
pgwf_context_has_label() {
  local item_json="$1" label="$2"
  jq -e --arg l "$label" '(.labels // []) | index($l) != null' <<<"$item_json" >/dev/null
}

# pgwf_context_label_value ITEM_JSON PREFIX -- the first label on ITEM_JSON
# starting with PREFIX, with PREFIX stripped (empty if none). Used for
# kind:<x> and component:<x> (both single-valued, Axis 3).
pgwf_context_label_value() {
  local item_json="$1" prefix="$2"
  jq -r --arg p "$prefix" \
    '(.labels // []) | map(select(startswith($p))) | (.[0] // "") | if . == "" then "" else ltrimstr($p) end' \
    <<<"$item_json"
}

# pgwf_context_title_terms TITLE -- TITLE's key terms (lowercased runs of
# 4+ alnum chars, deduped), one per line. Freedom boundary (this packet's
# own choice, same footing as the WI_APPLICABLE_DOCS keyword-match
# algorithm the Contract explicitly leaves open): the design specifies
# "title/key-term candidates" and "keyword match of the item's terms"
# without mandating an algorithm; this is a simple, dependency-free one.
pgwf_context_title_terms() {
  local title="$1"
  printf '%s' "$title" | tr '[:upper:]' '[:lower:]' | grep -oE '[a-z0-9]{4,}' | sort -u
}

# --- classification (Axis 1/2/3) ----------------------------------------

# pgwf_context_class CONFIG_JSON ITEM_JSON -- WI_CLASS
# (stage|question|container|legacy-human) [design: ## Components ->
# "Rendered prompt contract"]. A legacy `human` item is one carrying the
# human label but NOT the question label (Axis 2, "Legacy human items").
pgwf_context_class() {
  local config_json="$1" item_json="$2" question_label container_label human_label
  question_label="$(pgwf_config_label "$config_json" question question)"
  container_label="$(pgwf_config_label "$config_json" container container)"
  human_label="$(pgwf_config_label "$config_json" human human)"
  if pgwf_context_has_label "$item_json" "$question_label"; then
    printf 'question\n'
  elif pgwf_context_has_label "$item_json" "$container_label"; then
    printf 'container\n'
  elif pgwf_context_has_label "$item_json" "$human_label"; then
    printf 'legacy-human\n'
  else
    printf 'stage\n'
  fi
}

# pgwf_context_classification_stage CONFIG_JSON WORKFLOW -- WORKFLOW's
# entry stage, which doubles as "the workflow's default (classification)
# stage" for WI_KINDS/WI_TOPICS rendering [design: ## Components ->
# "Rendered prompt contract", the [rev7] WI_KINDS/WI_TOPICS paragraph].
pgwf_context_classification_stage() {
  local config_json="$1" workflow="$2" stages
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
  pgwf_workflow_entry_stage "$stages"
}

# pgwf_context_topics CONFIG_JSON ITEM_JSON -- the item's topic labels
# (labels that are ALSO keys of config.topics -- topics carry no prefix of
# their own, unlike kind/component [design: ## Configuration, the `topics`
# block; ## State model -> Axis 3]), one per line.
pgwf_context_topics() {
  local config_json="$1" item_json="$2" label
  while IFS= read -r label; do
    [[ -z $label ]] && continue
    pgwf_context_has_label "$item_json" "$label" && printf '%s\n' "$label"
  done < <(jq -r '.topics // {} | keys[]' <<<"$config_json")
}

# pgwf_context_kinds_rendered CONFIG_JSON -- config.kinds rendered WITH
# descriptions ("name: description", one per line) [design: ## State model
# -> Axis 3; ## Components "Rendered prompt contract" [rev7] note].
pgwf_context_kinds_rendered() {
  local config_json="$1"
  jq -r '.kinds // {} | to_entries[] | "\(.key): \(.value)"' <<<"$config_json"
}

# pgwf_context_topics_rendered CONFIG_JSON -- config.topics rendered WITH
# descriptions, same shape as kinds above.
pgwf_context_topics_rendered() {
  local config_json="$1"
  jq -r '.topics // {} | to_entries[] | "\(.key): \(.value)"' <<<"$config_json"
}

# --- data-file resolution (instructions/escalation/checklist/concerns) --

# pgwf_context_instructions_ref CONFIG_JSON WORKFLOW STAGE -- the stage's
# configured `instructions` value verbatim: "<built-in>" (the null
# workflow's own literal, [design: ## Configuration, the fenced JSON block
# after "Three terms"]), a data-overlay-relative path, or empty when the
# stage has none configured.
pgwf_context_instructions_ref() {
  local config_json="$1" workflow="$2" stage="$3" stages
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
  jq -r --arg s "$stage" '.[$s].instructions // empty' <<<"$stages"
}

# pgwf_context_escalation_ref CONFIG_JSON WORKFLOW STAGE -- same shape for
# the stage's `escalation` value (empty under the null workflow -- it has
# none [design: ## Configuration, same fenced block]).
pgwf_context_escalation_ref() {
  local config_json="$1" workflow="$2" stage="$3" stages
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
  jq -r --arg s "$stage" '.[$s].escalation // empty' <<<"$stages"
}

# pgwf_context_resolve_ref CONFIG_JSON REF -- resolves REF (an
# instructions/escalation ref) to its text: the literal "<built-in>" prints
# verbatim; a path resolves through pgwf_context_data_file (C-3); empty
# input or an unresolved path prints nothing and, for a non-empty
# unresolved path, warns on stderr (never a hard error -- mirrors C-5's
# "skipped with a warning, never an error" posture for concerns).
pgwf_context_resolve_ref() {
  local config_json="$1" ref="$2"
  if [[ -z $ref ]]; then
    return 0
  fi
  if [[ $ref == "<built-in>" ]]; then
    printf '%s' "$ref"
    return 0
  fi
  if ! pgwf_context_data_file "$config_json" "$ref"; then
    echo "pg-wi-flow: context: data file not found: $ref" >&2
    return 1
  fi
}

# pgwf_context_checklist_text CONFIG_JSON KIND -- checklists/<KIND>.md's
# text via the data overlay (C-3), or nothing when KIND is empty or the
# file does not resolve in either layer.
pgwf_context_checklist_text() {
  local config_json="$1" kind="$2"
  [[ -z $kind ]] && return 0
  pgwf_context_data_file "$config_json" "checklists/${kind}.md" 2>/dev/null
}

# pgwf_context_concern_names CONFIG_JSON WORKFLOW STAGE TOPICS_NEWLINE --
# the ordered concern-name list [design: ## Components -> "Rendered prompt
# contract", the [rev7] concern-ordering paragraph]: STAGE's own
# `concerns` array in config order, THEN topic-triggered concerns (from
# `concern_triggers`, keyed by each topic in TOPICS_NEWLINE) in canonical
# (sorted) order, deduped against the stage's own list and against each
# other. One name per line.
pgwf_context_concern_names() {
  local config_json="$1" workflow="$2" stage="$3" topics_nl="$4" stages
  stages="$(pgwf_workflow_stages_by_name "$config_json" "$workflow")"
  local -a stage_concerns=()
  local c
  while IFS= read -r c; do
    [[ -n $c ]] && stage_concerns+=("$c")
  done < <(jq -r --arg s "$stage" '.[$s].concerns[]? // empty' <<<"$stages")
  printf '%s\n' "${stage_concerns[@]}"

  local -a extra=()
  local topic
  while IFS= read -r topic; do
    [[ -z $topic ]] && continue
    while IFS= read -r c; do
      [[ -n $c ]] && extra+=("$c")
    done < <(jq -r --arg t "$topic" '.concern_triggers[$t][]? // empty' <<<"$config_json")
  done <<<"$topics_nl"

  local uniq_extra
  uniq_extra="$(printf '%s\n' "${extra[@]:-}" | sort -u)"
  while IFS= read -r c; do
    [[ -z $c ]] && continue
    local dup=0 s
    for s in "${stage_concerns[@]:-}"; do
      [[ $s == "$c" ]] && dup=1 && break
    done
    [[ $dup -eq 0 ]] && printf '%s\n' "$c"
  done <<<"$uniq_extra"
}

# pgwf_context_concern_text CONFIG_JSON CONCERN -- concerns/<CONCERN>.md's
# text via the data overlay, or nothing (status 1) when it does not
# resolve -- caller records CONCERN as skipped rather than erroring (C-5).
pgwf_context_concern_text() {
  local config_json="$1" concern="$2"
  pgwf_context_data_file "$config_json" "concerns/${concern}.md"
}

# --- duplicates / related / applicable docs -----------------------------

# pgwf_context_duplicates CONFIG_JSON ID ITEM_JSON -- title/key-term
# candidates [design: ## Components -> duplicates row]: open items whose
# title contains one of ID's key terms, excluding ID itself. Compact JSON
# array of {id,title}.
pgwf_context_duplicates() {
  local id="$2" item_json="$3" title terms term out
  title="$(jq -r '.title // empty' <<<"$item_json")"
  terms="$(pgwf_context_title_terms "$title")"
  out='[]'
  while IFS= read -r term; do
    [[ -z $term ]] && continue
    local hits
    hits="$(pgwf_tracker_list --title-contains "$term" --status open,in_progress,blocked,deferred --json)" || continue
    out="$(jq -c -n --argjson a "$out" --argjson b "$hits" '$a + $b')"
  done <<<"$terms"
  jq -c --arg id "$id" '[.[] | select(.id != $id)] | unique_by(.id) | map({id, title})' <<<"$out"
}

# pgwf_context_related CONFIG_JSON ID ITEM_JSON -- same-component open AND
# recently-closed candidates [design: ## State model -> Axis 3,
# "`duplicates` returns same-component open and recently closed items as
# related candidates"]. "Recently closed" window: 30 days (this packet's
# own choice -- the design names the behavior, not the window).
pgwf_context_related() {
  local config_json="$1" id="$2" item_json="$3" component_prefix component label cutoff open_hits closed_hits
  component_prefix="$(pgwf_config_label "$config_json" component_prefix 'component:')"
  component="$(pgwf_context_label_value "$item_json" "$component_prefix")"
  if [[ -z $component ]]; then
    printf '[]'
    return 0
  fi
  label="${component_prefix}${component}"
  open_hits="$(pgwf_tracker_list --label-any "$label" --status open,in_progress,blocked,deferred --json)" || open_hits='[]'
  closed_hits="$(pgwf_tracker_list --label-any "$label" --status closed --json)" || closed_hits='[]'
  cutoff="$(date -u -d '-30 days' +%Y-%m-%dT%H:%M:%SZ)"
  closed_hits="$(jq -c --arg cutoff "$cutoff" '[.[] | select((.closed_at // .updated_at // "") >= $cutoff)]' <<<"$closed_hits")"
  jq -c -n --arg id "$id" --argjson a "$open_hits" --argjson b "$closed_hits" \
    '($a + $b) as $all | [$all[] | select(.id != $id)] | unique_by(.id) | map({id, title})'
}

# pgwf_context_docs CONFIG_JSON ITEM_JSON -- WI_APPLICABLE_DOCS: keyword
# match of the item's terms over rule/ADR/plan titles and headings under
# docs_search.roots, capped at docs_search.max [design: ## Configuration,
# docs_search fenced example; ## The flow -> "Stage: groom" -> concern
# "conformance" bullet, verbatim]. Freedom boundary (Contract, explicit):
# the matching algorithm itself is this packet's own design choice.
pgwf_context_docs() {
  local config_json="$1" item_json="$2" repo_root title terms max count=0
  repo_root="$(pgwf_context_repo_root)"
  title="$(jq -r '.title // empty' <<<"$item_json")"
  terms="$(pgwf_context_title_terms "$title")"
  max="$(jq -r '.docs_search.max // 8' <<<"$config_json")"
  if [[ -z $terms ]]; then
    printf '[]'
    return 0
  fi
  local -a matches=()
  local root
  while IFS= read -r root; do
    [[ -z $root ]] && continue
    local dir="$repo_root/$root"
    [[ -d $dir ]] || continue
    local file
    while IFS= read -r -d '' file; do
      local heading
      while IFS= read -r heading; do
        local text lower term matched=0
        text="${heading#"${heading%%[!#]*}"}"
        text="${text#"${text%%[![:space:]]*}"}"
        lower="$(printf '%s' "$text" | tr '[:upper:]' '[:lower:]')"
        while IFS= read -r term; do
          [[ -z $term ]] && continue
          if [[ $lower == *"$term"* ]]; then
            matched=1
            break
          fi
        done <<<"$terms"
        if [[ $matched -eq 1 ]]; then
          matches+=("$(jq -c -n --arg p "${file#"$repo_root"/}" --arg h "$text" '{path:$p, heading:$h}')")
          count=$((count + 1))
          [[ $count -ge $max ]] && break 2
        fi
      done < <(grep -E '^#{1,6}[[:space:]]' "$file" 2>/dev/null)
    done < <(find "$dir" -type f -name '*.md' -print0 2>/dev/null | sort -z)
  done < <(jq -r '.docs_search.roots[]? // empty' <<<"$config_json")
  if [[ ${#matches[@]} -eq 0 ]]; then
    printf '[]'
  else
    printf '%s\n' "${matches[@]}" | jq -cs '.'
  fi
}

# --- premise check --------------------------------------------------------

# pgwf_context_premise ITEM_JSON -- ITEM_JSON's premise (metadata.premise),
# or empty [design: ## The flow -> "Stage: groom", the researcher findings
# bullet: "`--premise` naming any concrete external target"].
pgwf_context_premise() {
  local item_json="$1"
  jq -r '.metadata.premise // empty' <<<"$item_json"
}

# pgwf_context_premise_stale CONFIG_JSON ITEM_JSON PREMISE -- true (status
# 0) when a CLOSED kind:remove item, closed after ITEM_JSON's last groom,
# names the same target as PREMISE [design: ## The flow -> "Stage: verify",
# the premise-check paragraph -- general mechanism, implemented even though
# kind:remove classification and groom itself are phase-2/homelab-workflow
# concerns]. "Last groom" = metadata.groomed_at, falling back to
# created_at when groom (phase 2) has never annotated it. "Names the same
# target" = the closed item's title or its own premise metadata contains
# PREMISE as a case-insensitive substring -- this matching rule, like the
# docs-search one above, is this packet's own choice absent a mandated
# algorithm.
pgwf_context_premise_stale() {
  local config_json="$1" item_json="$2" premise="$3" kind_prefix remove_label last_groom lower_premise closed_hits
  [[ -z $premise ]] && return 1
  kind_prefix="$(pgwf_config_label "$config_json" kind_prefix 'kind:')"
  remove_label="${kind_prefix}remove"
  last_groom="$(jq -r '.metadata.groomed_at // .created_at // empty' <<<"$item_json")"
  lower_premise="$(printf '%s' "$premise" | tr '[:upper:]' '[:lower:]')"
  closed_hits="$(pgwf_tracker_list --label-any "$remove_label" --status closed --json)" || return 1
  jq -e --arg cutoff "$last_groom" --arg p "$lower_premise" \
    '[.[] | select((.closed_at // .updated_at // "") > $cutoff) |
       select(((.title // "") | ascii_downcase | contains($p)) or
              ((.metadata.premise // "") | ascii_downcase | contains($p)))] | length > 0' \
    <<<"$closed_hits" >/dev/null
}

# --- siblings / blocked parents / same fingerprint / questions ----------

# pgwf_context_siblings ID PARENT -- PARENT's other (open) children,
# excluding ID itself. Compact JSON array of {id,title}; [] when PARENT is
# empty.
pgwf_context_siblings() {
  local id="$1" parent="$2" kids
  if [[ -z $parent ]]; then
    printf '[]'
    return 0
  fi
  kids="$(pgwf_tracker_open_children "$parent")" || {
    printf '[]'
    return 0
  }
  jq -c --arg id "$id" '[.[] | select(.id != $id)] | map({id, title})' <<<"$kids"
}

# pgwf_context_blocked_parents ID -- for a question item, every parent it
# blocks (the `blocks` dependency edges escalate created, [design: ## State
# model -> Axis 2, "Fingerprint dedupe" -- "the resolver's render shows
# every attached parent"]). Compact JSON array of {id,title}.
pgwf_context_blocked_parents() {
  local id="$1" deps
  deps="$(pgwf_tracker_dependents "$id" blocks)" || {
    printf '[]'
    return 0
  }
  jq -c 'map({id, title})' <<<"$deps"
}

# pgwf_context_same_fingerprint CONFIG_JSON ID ITEM_JSON -- every OTHER
# open question sharing ITEM_JSON's fingerprint metadata [design: ## State
# model -> Axis 2, "Fingerprint dedupe", the WI_SAME_FINGERPRINT mitigation
# paragraph]. [] when ITEM_JSON carries no fingerprint.
pgwf_context_same_fingerprint() {
  local config_json="$1" id="$2" item_json="$3" question_label fp hits
  fp="$(jq -r '.metadata.fingerprint // empty' <<<"$item_json")"
  if [[ -z $fp ]]; then
    printf '[]'
    return 0
  fi
  question_label="$(pgwf_config_label "$config_json" question question)"
  hits="$(pgwf_tracker_list --label-any "$question_label" --metadata-field "fingerprint=$fp" --status open --json)" || {
    printf '[]'
    return 0
  }
  jq -c --arg id "$id" '[.[] | select(.id != $id)] | map({id, title})' <<<"$hits"
}

# pgwf_context_questions CONFIG_JSON ID -- ID's own open question children
# [design: ## Components -> "Rendered prompt contract", WI_QUESTIONS].
# Compact JSON array of {id,title}.
pgwf_context_questions() {
  local config_json="$1" id="$2" question_label kids
  question_label="$(pgwf_config_label "$config_json" question question)"
  kids="$(pgwf_tracker_open_children "$id")" || {
    printf '[]'
    return 0
  }
  jq -c --arg l "$question_label" '[.[] | select((.labels // []) | index($l) != null)] | map({id, title})' <<<"$kids"
}

# pgwf_context_worktree ITEM_JSON -- ITEM_JSON's own metadata.wi_worktree,
# or empty [design: ## Components -> "Rendered prompt contract",
# WI_WORKTREE; ## The flow -> "Stage: implement", "does exactly that in
# WI_WORKTREE"]. Not inherited from an ancestor: worktree ASSIGNMENT is a
# decompose (phase 2) decision per item, out of this phase's scope -- the
# mechanism (reading whatever is stamped) is what this phase builds.
pgwf_context_worktree() {
  local item_json="$1"
  jq -r '.metadata.wi_worktree // empty' <<<"$item_json"
}

# pgwf_context_round ITEM_JSON -- ITEM_JSON's current review round
# (metadata.wi_round, default 0 -- round/record-verdict is packet P3's
# scope; this phase only ever sees the default).
pgwf_context_round() {
  local item_json="$1"
  jq -r '.metadata.wi_round // 0' <<<"$item_json"
}

# pgwf_context_must_escalate CONFIG_JSON ROUND -- "true" when ROUND has
# reached config.iteration_bound [design: ## The flow -> "Per-stage loop",
# step 2: "When the counter reaches iteration_bound without ready, the CLI
# prints WI_MUST_ESCALATE=true"], else "false".
pgwf_context_must_escalate() {
  local config_json="$1" round="$2" bound
  bound="$(pgwf_config_iteration_bound "$config_json")"
  if [[ $round -ge $bound ]]; then
    printf 'true\n'
  else
    printf 'false\n'
  fi
}

# --- top-level: facts, plain lines, full render -------------------------

# pgwf_context_stage_for_class CONFIG_JSON ID PARENT CLASS -- ID's
# rendered WI_STAGE: for a question item, this is the PARENT's effective
# stage ("a question's stage is its parent's", Axis 2); otherwise ID's own
# effective stage.
pgwf_context_stage_for_class() {
  local config_json="$1" id="$2" parent="$3" class="$4"
  if [[ $class == question && -n $parent ]]; then
    pgwf_effective_stage_name "$config_json" "$parent"
  else
    pgwf_effective_stage_name "$config_json" "$id"
  fi
}

# pgwf_context_facts CONFIG_JSON ID -- builds every fact this render engine
# needs as one compact JSON object on stdout. The single source both
# pgwf_context_format_lines and pgwf_context_format_render read from, so
# the two output shapes can never disagree on an underlying value.
pgwf_context_facts() {
  local config_json="$1" id="$2"
  local item_json class stage workflow kind_prefix component_prefix kind component
  local parent worktree premise stale round must_escalate legacy_human type title description
  local classification_stage kinds_rendered topics_rendered topics_nl
  local instructions_ref escalation_ref checklist_text_val
  local duplicates related docs siblings blocked_parents same_fp questions concern_names_nl

  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  class="$(pgwf_context_class "$config_json" "$item_json")"
  parent="$(jq -r '.parent // empty' <<<"$item_json")"
  stage="$(pgwf_context_stage_for_class "$config_json" "$id" "$parent" "$class")" || return 1
  workflow="$(pgwf_workflow_for "$config_json" "$id")" || return 1
  type="$(jq -r '.issue_type // empty' <<<"$item_json")"
  title="$(jq -r '.title // empty' <<<"$item_json")"
  description="$(jq -r '.description // empty' <<<"$item_json")"

  kind_prefix="$(pgwf_config_label "$config_json" kind_prefix 'kind:')"
  component_prefix="$(pgwf_config_label "$config_json" component_prefix 'component:')"
  kind="$(pgwf_context_label_value "$item_json" "$kind_prefix")"
  component="$(pgwf_context_label_value "$item_json" "$component_prefix")"

  classification_stage="$(pgwf_context_classification_stage "$config_json" "$workflow")"
  kinds_rendered=""
  topics_rendered=""
  topics_nl="$(pgwf_context_topics "$config_json" "$item_json")"
  if [[ -n $classification_stage && $stage == "$classification_stage" ]]; then
    kinds_rendered="$(pgwf_context_kinds_rendered "$config_json")"
    topics_rendered="$(pgwf_context_topics_rendered "$config_json")"
  fi

  round="$(pgwf_context_round "$item_json")"
  must_escalate="$(pgwf_context_must_escalate "$config_json" "$round")"
  legacy_human=false
  [[ $class == legacy-human ]] && legacy_human=true

  worktree="$(pgwf_context_worktree "$item_json")"
  premise="$(pgwf_context_premise "$item_json")"
  stale=false
  if [[ -n $premise ]] && pgwf_context_premise_stale "$config_json" "$item_json" "$premise"; then
    stale=true
  fi

  instructions_ref="$(pgwf_context_instructions_ref "$config_json" "$workflow" "$stage")"
  escalation_ref="$(pgwf_context_escalation_ref "$config_json" "$workflow" "$stage")"
  checklist_text_val="$(pgwf_context_checklist_text "$config_json" "$kind")"

  concern_names_nl="$(pgwf_context_concern_names "$config_json" "$workflow" "$stage" "$topics_nl")"
  local -a concerns=() skipped=()
  local cname ctext
  while IFS= read -r cname; do
    [[ -z $cname ]] && continue
    if ctext="$(pgwf_context_concern_text "$config_json" "$cname")"; then
      concerns+=("$(jq -c -n --arg n "$cname" --arg t "$ctext" '{name:$n, text:$t}')")
    else
      echo "pg-wi-flow: context: concern file not found, skipping: $cname" >&2
      skipped+=("$cname")
    fi
  done <<<"$concern_names_nl"

  duplicates="$(pgwf_context_duplicates "$config_json" "$id" "$item_json")"
  related="$(pgwf_context_related "$config_json" "$id" "$item_json")"
  docs="$(pgwf_context_docs "$config_json" "$item_json")"
  siblings="$(pgwf_context_siblings "$id" "$parent")"
  blocked_parents='[]'
  same_fp='[]'
  if [[ $class == question ]]; then
    blocked_parents="$(pgwf_context_blocked_parents "$id")"
    same_fp="$(pgwf_context_same_fingerprint "$config_json" "$id" "$item_json")"
  fi
  questions="$(pgwf_context_questions "$config_json" "$id")"

  local concerns_json skipped_json
  if [[ ${#concerns[@]} -eq 0 ]]; then
    concerns_json='[]'
  else
    concerns_json="$(printf '%s\n' "${concerns[@]}" | jq -cs '.')"
  fi
  skipped_json="$(printf '%s\n' "${skipped[@]:-}" | jq -Rn '[inputs | select(length > 0)]')"

  jq -cn \
    --arg id "$id" --arg type "$type" --arg class "$class" --arg stage "$stage" \
    --arg workflow "$workflow" --arg kind "$kind" --arg component "$component" \
    --arg kinds_rendered "$kinds_rendered" --arg topics_rendered "$topics_rendered" \
    --arg round "$round" --arg must_escalate "$must_escalate" --argjson legacy_human "$legacy_human" \
    --arg parent "$parent" --arg worktree "$worktree" --arg premise "$premise" --argjson premise_stale "$stale" \
    --arg instructions_ref "$instructions_ref" --arg escalation_ref "$escalation_ref" \
    --arg checklist "$checklist_text_val" --argjson concerns "$concerns_json" --argjson skipped_concerns "$skipped_json" \
    --argjson duplicates "$duplicates" --argjson related "$related" --argjson applicable_docs "$docs" \
    --argjson siblings "$siblings" --argjson blocked_parents "$blocked_parents" --argjson same_fingerprint "$same_fp" \
    --argjson questions "$questions" --arg title "$title" --arg description "$description" \
    '{
      id: $id, type: $type, class: $class, stage: $stage, workflow: $workflow,
      kind: $kind, component: $component,
      kinds_rendered: $kinds_rendered, topics_rendered: $topics_rendered,
      round: $round, must_escalate: $must_escalate, legacy_human: $legacy_human,
      parent: $parent, worktree: $worktree, premise: $premise, premise_stale: $premise_stale,
      instructions_ref: $instructions_ref, escalation_ref: $escalation_ref,
      checklist: $checklist, concerns: $concerns, skipped_concerns: $skipped_concerns,
      duplicates: $duplicates, related: $related, applicable_docs: $applicable_docs,
      siblings: $siblings, blocked_parents: $blocked_parents, same_fingerprint: $same_fingerprint,
      questions: $questions, title: $title, description: $description, lessons: ""
    }'
}

# pgwf_context_join_list JSON_ARRAY -- "id:title;id2:title2" for a compact
# JSON array of {id,title} objects [design: ## Components -> "Rendered
# prompt contract", the WI_DUPLICATES/WI_APPLICABLE_DOCS format notes].
pgwf_context_join_list() {
  jq -r 'map("\(.id):\(.title)") | join(";")' <<<"$1"
}

# pgwf_context_join_docs JSON_ARRAY -- "path:heading;…" for
# WI_APPLICABLE_DOCS's {path,heading} shape.
pgwf_context_join_docs() {
  jq -r 'map("\(.path):\(.heading)") | join(";")' <<<"$1"
}

# pgwf_context_format_lines FACTS_JSON -- the plain `context <id>` output:
# one "WI_KEY=value" line per field, in the design's own field order
# [design: ## Components -> "Rendered prompt contract", the verbatim field
# list]. Multi-line content (instructions/checklist/concern text) is
# reported by REFERENCE here (a path, "<built-in>", or a concern/kind
# name) rather than expanded -- expansion is --render's job
# (pgwf_context_format_render), keeping this a genuine one-line-per-field
# dump.
pgwf_context_format_lines() {
  local f="$1"
  local id type class stage workflow kind kinds_rendered topics_rendered component
  local round must_escalate legacy_human parent siblings blocked_parents same_fp worktree
  local premise premise_stale instructions_ref escalation_ref checklist concerns skipped
  local duplicates related applicable_docs questions

  id="$(jq -r '.id' <<<"$f")"
  type="$(jq -r '.type' <<<"$f")"
  class="$(jq -r '.class' <<<"$f")"
  stage="$(jq -r '.stage' <<<"$f")"
  workflow="$(jq -r '.workflow' <<<"$f")"
  kind="$(jq -r '.kind' <<<"$f")"
  kinds_rendered="$(jq -r '.kinds_rendered' <<<"$f")"
  topics_rendered="$(jq -r '.topics_rendered' <<<"$f")"
  component="$(jq -r '.component' <<<"$f")"
  round="$(jq -r '.round' <<<"$f")"
  must_escalate="$(jq -r '.must_escalate' <<<"$f")"
  legacy_human="$(jq -r '.legacy_human' <<<"$f")"
  parent="$(jq -r '.parent' <<<"$f")"
  siblings="$(pgwf_context_join_list "$(jq -c '.siblings' <<<"$f")")"
  blocked_parents="$(pgwf_context_join_list "$(jq -c '.blocked_parents' <<<"$f")")"
  same_fp="$(jq -r '.same_fingerprint | map(.id) | join(";")' <<<"$f")"
  worktree="$(jq -r '.worktree' <<<"$f")"
  premise="$(jq -r '.premise' <<<"$f")"
  premise_stale="$(jq -r '.premise_stale' <<<"$f")"
  instructions_ref="$(jq -r '.instructions_ref' <<<"$f")"
  escalation_ref="$(jq -r '.escalation_ref' <<<"$f")"
  checklist="$(jq -r '.checklist != ""' <<<"$f")"
  concerns="$(jq -r '.concerns | map(.name) | join(";")' <<<"$f")"
  skipped="$(jq -r '.skipped_concerns | join(";")' <<<"$f")"
  duplicates="$(pgwf_context_join_list "$(jq -c '.duplicates' <<<"$f")")"
  related="$(pgwf_context_join_list "$(jq -c '.related' <<<"$f")")"
  applicable_docs="$(pgwf_context_join_docs "$(jq -c '.applicable_docs' <<<"$f")")"
  questions="$(pgwf_context_join_list "$(jq -c '.questions' <<<"$f")")"

  cat <<LINES
WI_ID=$id
WI_TYPE=$type
WI_CLASS=$class
WI_STAGE=$stage
WI_WORKFLOW=$workflow
WI_KIND=$kind
WI_KINDS=$kinds_rendered
WI_TOPICS=$topics_rendered
WI_COMPONENT=$component
WI_ROUND=$round
WI_MUST_ESCALATE=$must_escalate
WI_LEGACY_HUMAN=$legacy_human
WI_PARENT=$parent
WI_SIBLINGS=$siblings
WI_BLOCKED_PARENTS=$blocked_parents
WI_SAME_FINGERPRINT=$same_fp
WI_WORKTREE=$worktree
WI_PREMISE=$premise
WI_PREMISE_STALE=$premise_stale
WI_INSTRUCTIONS=$instructions_ref
WI_ESCALATION=$escalation_ref
WI_CHECKLIST=$checklist
WI_CONCERNS=$concerns
WI_SKIPPED_CONCERNS=$skipped
WI_DUPLICATES=$duplicates
WI_RELATED=$related
WI_APPLICABLE_DOCS=$applicable_docs
WI_LESSONS=
WI_QUESTIONS=$questions
LINES
}

# pgwf_context_render_include CONFIG_JSON -- config.render.include, one
# name per line, defaulting to the design's own fenced example list when
# absent [design: ## Configuration, the `render` block].
pgwf_context_render_include() {
  local config_json="$1" list
  list="$(jq -r '.render.include[]? // empty' <<<"$config_json")"
  if [[ -z $list ]]; then
    printf '%s\n' title description acceptance design labels kind component premise \
      checklist concerns duplicates applicable_docs lessons questions
  else
    printf '%s\n' "$list"
  fi
}

# pgwf_context_include CONFIG_JSON FIELD -- true if FIELD is on the
# render.include allowlist (C-8: item-CONTENT fields only -- structural
# fields never call this).
pgwf_context_include() {
  local config_json="$1" field="$2"
  pgwf_context_render_include "$config_json" | grep -qxF "$field"
}

# pgwf_context_format_render CONFIG_JSON FACTS_JSON ROLE CONCERN GAP --
# the fully assembled prompt: STATIC text first (instructions, checklist,
# concern text), item-SPECIFIC text last (WI_* structural fields always;
# item-content fields gated by render.include) [design: ## Components ->
# "Rendered prompt contract", the [rev7] "STATIC text first" paragraph].
# ROLE == researcher gets the narrow scope mandated in "Binding decisions"
# (id, title, the gap object, applicable docs only) regardless of
# render.include.
pgwf_context_format_render() {
  local config_json="$1" f="$2" role="$3" concern="$4" gap="$5"

  if [[ $role == researcher ]]; then
    printf 'WI_ID=%s\n' "$(jq -r '.id' <<<"$f")"
    printf 'title: %s\n' "$(jq -r '.title' <<<"$f")"
    printf 'gap: %s\n' "$gap"
    printf 'WI_APPLICABLE_DOCS=%s\n' "$(pgwf_context_join_docs "$(jq -c '.applicable_docs' <<<"$f")")"
    return 0
  fi

  local instructions_ref instructions_text checklist_text concerns_json
  instructions_ref="$(jq -r '.instructions_ref' <<<"$f")"
  instructions_text="$(pgwf_context_resolve_ref "$config_json" "$instructions_ref" 2>/dev/null || true)"
  checklist_text="$(jq -r '.checklist' <<<"$f")"
  concerns_json="$(jq -c '.concerns' <<<"$f")"

  # --- static block: role/stage instructions, checklist, concern text ---
  if [[ -n $instructions_text ]]; then
    printf '## Instructions\n\n%s\n\n' "$instructions_text"
  fi
  if [[ -n $role && -n $concern ]]; then
    printf '## Role\n\nreviewer, concern: %s\n\n' "$concern"
  fi
  if pgwf_context_include "$config_json" checklist && [[ -n $checklist_text ]]; then
    printf '## Checklist\n\n%s\n\n' "$checklist_text"
  fi
  if pgwf_context_include "$config_json" concerns; then
    local n
    n="$(jq 'length' <<<"$concerns_json")"
    if [[ $n -gt 0 ]]; then
      printf '## Concerns\n\n'
      jq -r '.[] | "### \(.name)\n\n\(.text)\n"' <<<"$concerns_json"
    fi
  fi

  # --- item-specific block: structural fields always, content fields per
  # render.include ---
  printf '## Item\n\n'
  pgwf_context_format_lines "$f"
  if pgwf_context_include "$config_json" title; then
    printf 'title: %s\n' "$(jq -r '.title' <<<"$f")"
  fi
  if pgwf_context_include "$config_json" description; then
    printf 'description: %s\n' "$(jq -r '.description' <<<"$f")"
  fi
  if pgwf_context_include "$config_json" premise; then
    printf 'premise: %s\n' "$(jq -r '.premise' <<<"$f")"
  fi
  if pgwf_context_include "$config_json" duplicates; then
    printf 'duplicates: %s\n' "$(pgwf_context_join_list "$(jq -c '.duplicates' <<<"$f")")"
  fi
  if pgwf_context_include "$config_json" applicable_docs; then
    printf 'applicable_docs: %s\n' "$(pgwf_context_join_docs "$(jq -c '.applicable_docs' <<<"$f")")"
  fi
  if pgwf_context_include "$config_json" lessons; then
    printf 'lessons: %s\n' "$(jq -r '.lessons' <<<"$f")"
  fi
  if pgwf_context_include "$config_json" questions; then
    printf 'questions: %s\n' "$(pgwf_context_join_list "$(jq -c '.questions' <<<"$f")")"
  fi
}

# pgwf_context_cmd [--render] ID [--role R [--concern C | --gap G]] -- the
# `context` verb's entry point, called by pg-wi-flow.sh/pg-wi-flow.bash and
# (per Contract) by `claim` in the same call.
pgwf_context_cmd() {
  local render=0 id="" role="" concern="" gap=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --render)
      render=1
      shift
      ;;
    --role)
      role="$2"
      shift 2
      ;;
    --concern)
      concern="$2"
      shift 2
      ;;
    --gap)
      gap="$2"
      shift 2
      ;;
    *)
      if [[ -z $id ]]; then
        id="$1"
        shift
      else
        echo "pg-wi-flow: context: unknown argument: $1" >&2
        return 1
      fi
      ;;
    esac
  done
  if [[ -z $id ]]; then
    echo "pg-wi-flow: context: missing ID" >&2
    return 1
  fi

  local config_json facts
  config_json="$(pgwf_config_effective)" || return 1
  facts="$(pgwf_context_facts "$config_json" "$id")" || return 1

  if [[ $render -eq 1 ]]; then
    pgwf_context_format_render "$config_json" "$facts" "$role" "$concern" "$gap"
  else
    pgwf_context_format_lines "$facts"
  fi
}

# --- explain / history / duplicates / docs (thin wrappers over the same
# facts engine) ------------------------------------------------------------

# pgwf_cmd_explain ID -- classification, stage, workflow, kind, component,
# premise state, open questions, round count, why it is/is not in each
# pool, current holder / time since claim / unclaimed [design: ##
# Components -> explain row].
pgwf_cmd_explain() {
  local id="$1"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: explain: missing ID" >&2
    return 1
  fi
  local config_json facts item_json assignee updated_at holder
  config_json="$(pgwf_config_effective)" || return 1
  facts="$(pgwf_context_facts "$config_json" "$id")" || return 1
  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  assignee="$(jq -r '.assignee // empty' <<<"$item_json")"
  updated_at="$(jq -r '.updated_at // empty' <<<"$item_json")"
  if [[ -z $assignee ]]; then
    holder=unclaimed
  else
    holder="$assignee (since $updated_at)"
  fi

  pgwf_context_format_lines "$facts"
  printf 'WI_HOLDER=%s\n' "$holder"
}

# pgwf_cmd_history ID -- the verdict and round trail [design: ##
# Components -> history row]. round/record-verdict is packet P3's scope;
# under this phase's null workflow no round is ever recorded, so this
# prints an empty trail -- the general mechanism (read whatever rounds
# exist in metadata), not a null-workflow special case.
pgwf_cmd_history() {
  local id="$1"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: history: missing ID" >&2
    return 1
  fi
  local item_json
  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  jq -c '.metadata.wi_verdicts // []' <<<"$item_json"
}

# pgwf_cmd_duplicates ID -- WI_DUPLICATES/WI_RELATED on their own [design:
# ## Components -> duplicates row].
pgwf_cmd_duplicates() {
  local id="$1"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: duplicates: missing ID" >&2
    return 1
  fi
  local config_json item_json
  config_json="$(pgwf_config_effective)" || return 1
  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  printf 'WI_DUPLICATES=%s\n' "$(pgwf_context_join_list "$(pgwf_context_duplicates "$config_json" "$id" "$item_json")")"
  printf 'WI_RELATED=%s\n' "$(pgwf_context_join_list "$(pgwf_context_related "$config_json" "$id" "$item_json")")"
}

# pgwf_cmd_docs ID -- the applicable-docs candidate search on its own
# [design: ## Components -> docs row].
pgwf_cmd_docs() {
  local id="$1"
  if [[ -z $id ]]; then
    echo "pg-wi-flow: docs: missing ID" >&2
    return 1
  fi
  local config_json item_json
  config_json="$(pgwf_config_effective)" || return 1
  item_json="$(pgwf_tracker_show_json "$id")" || return 1
  printf 'WI_APPLICABLE_DOCS=%s\n' "$(pgwf_context_join_docs "$(pgwf_context_docs "$config_json" "$item_json")")"
}
