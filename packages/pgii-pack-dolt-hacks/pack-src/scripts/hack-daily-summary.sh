#!/usr/bin/env bash
# hack-daily-summary.sh — see ../orders/hack-daily-summary.toml for context.
set -euo pipefail

# ── configuration (override-able via env for testing) ─────────────────────
: "${GC_CITY:?GC_CITY must be set (this script expects gc supervisor/order context)}"
: "${CURSOR_FILE:=$GC_CITY/.gc/state/daily-summary-last-run}"
: "${EVENTS_FILE:=$GC_CITY/.gc/events.jsonl}"
: "${TRACE_FILE:=$GC_CITY/.gc/runtime/control-dispatcher-trace.log}"
: "${CITY_TOML:=$GC_CITY/city.toml}"
: "${GAP_CAP_SECONDS:=604800}" # 7 days
: "${POLISH_TIMEOUT_SECONDS:=60}"
: "${POLISH_MODEL:=claude-haiku-4-5}"
: "${RECIPIENT:=operator}"
: "${DATE_BIN:=/bin/date}" # BSD `date -r` semantics required for epoch→ISO conversion.

POLISH_PROMPT='You are summarizing a Gas City operator overnight activity log.
The input is structured markdown with sections: Bead activity, PR activity,
Agent health, Order anomalies. Render a 250-400 word narrative for an
operator skimming at 7am. Lead with the most important thing
(anomalies > unmerged blockers > merges/closures > volume stats).
Use short paragraphs and inline bullets. Do NOT introduce any fact not
present in the input. Do not suggest actions. Do not add a preamble or
sign-off. Markdown OK.'

# ── cursor (Task 2 fills these in) ────────────────────────────────────────
read_cursor() {
  local now ts default_start
  now=$(date +%s)
  default_start=$((now - 86400))
  if [[ -r $CURSOR_FILE ]]; then
    ts="$(head -n1 "$CURSOR_FILE" 2>/dev/null || true)"
    if [[ $ts =~ ^[0-9]+$ ]]; then
      # Clamp gaps older than GAP_CAP_SECONDS to keep token cost bounded.
      if ((now - ts > GAP_CAP_SECONDS)); then
        echo $((now - GAP_CAP_SECONDS))
      else
        echo "$ts"
      fi
      return 0
    fi
  fi
  echo "$default_start"
}

write_cursor() {
  local ts="$1"
  mkdir -p "$(dirname "$CURSOR_FILE")"
  local tmp="$CURSOR_FILE.tmp"
  printf '%s\n' "$ts" >"$tmp"
  mv "$tmp" "$CURSOR_FILE"
}

# ── gather (Tasks 3-6 fill these in) ──────────────────────────────────────
# Args: $1 = start-epoch, $2 = output file path
gather_beads() {
  local start="$1" out="$2"
  local start_iso
  start_iso="$("$DATE_BIN" -u -r "$start" +'%Y-%m-%dT%H:%M:%SZ')"

  {
    echo
    echo "## Bead activity (since $start_iso)"
    echo
  } >>"$out"

  local closed_json created_json open_json
  if ! closed_json="$(gc bd list --closed-after "$start_iso" --json 2>/dev/null)"; then
    echo "_(section failed: gc bd list --closed-after returned non-zero)_" >>"$out"
    return 0
  fi
  if ! created_json="$(gc bd list --created-after "$start_iso" --json 2>/dev/null)"; then
    created_json="[]"
  fi
  if ! open_json="$(gc bd list --status=open --json 2>/dev/null)"; then
    open_json="[]"
  fi

  local closed_count created_count
  closed_count="$(jq 'length' <<<"$closed_json")"
  created_count="$(jq 'length' <<<"$created_json")"

  {
    echo "- Closed: $closed_count"
    echo "- Created: $created_count"
    echo
    echo "### Notable closures"
    jq -r '.[] | "- [\(.issue_type)] \(.id) — \(.title)"' <<<"$closed_json" | head -20
    echo
    echo "### Outstanding escalations / feedback"
    jq -r '
      .[]
      | select(.labels // [] | any(. | test("^(escalation|feedback)$")))
      | "- [\(.issue_type)] \(.id) — \(.title)"
    ' <<<"$open_json" | head -20
  } >>"$out"
}

gather_prs() {
  local start="$1" out="$2"
  local start_date
  start_date="$("$DATE_BIN" -u -r "$start" +'%Y-%m-%d')"
  local rigs_json
  rigs_json="$(gc rig list --json 2>/dev/null || echo '{"rigs":[]}')"

  {
    echo
    echo "## PR activity (updated since $start_date)"
    echo
  } >>"$out"

  # Derive owner/repo from each rig's git remote.origin.url. Skip rigs
  # without a github remote.
  local any_emitted=0
  local rig_paths
  rig_paths="$(jq -r '.rigs[]? | .path' <<<"$rigs_json")"
  local merged_total=0 open_total=0

  while IFS= read -r path; do
    [[ -z $path ]] && continue
    local origin
    origin="$(git -C "$path" config --get remote.origin.url 2>/dev/null || true)"
    [[ -z $origin ]] && continue
    # Parse github URL → owner/repo.
    local slug
    slug="$(sed -E 's#(git@|https?://)github\.com[/:]([^/]+/[^/.]+)(\.git)?#\2#' <<<"$origin")"
    [[ $slug == "$origin" ]] && continue # Not a github remote.

    any_emitted=1
    local prs_json
    if ! prs_json="$(gh pr list --repo "$slug" \
      --search "updated:>=$start_date" \
      --state all \
      --json number,title,state,mergedAt,author,reviewDecision,url \
      2>/dev/null)"; then
      echo "- $slug: _(gh failed)_" >>"$out"
      continue
    fi
    local merged_n open_n
    merged_n="$(jq '[.[] | select(.state == "MERGED")] | length' <<<"$prs_json")"
    open_n="$(jq '[.[] | select(.state == "OPEN")]   | length' <<<"$prs_json")"
    merged_total=$((merged_total + merged_n))
    open_total=$((open_total + open_n))

    {
      echo "### $slug"
      echo "- Merged: $merged_n"
      echo "- Open: $open_n"
      jq -r '.[] | "  - [#\(.number)] \(.state) — \(.title) (\(.url))"' <<<"$prs_json" | head -20
      echo
    } >>"$out"
  done <<<"$rig_paths"

  if ((any_emitted == 0)); then
    echo "_(No rigs with GitHub remotes found.)_" >>"$out"
    return 0
  fi

  {
    echo "**Totals — Merged: $merged_total, Open: $open_total**"
  } >>"$out"
}
gather_agents() {
  local start="$1" out="$2"
  local now
  now=$(date +%s)
  local secs=$((now - start))
  local since="${secs}s"

  {
    echo
    echo "## Agent health"
    echo
  } >>"$out"

  # Single call; jq filters by type from the unified stream.
  local events_out
  events_out="$(gc events --since "$since" 2>/dev/null || true)"

  local crashes idle quars
  crashes="$(jq -s 'map(select(.type == "session.crashed"))     | length' <<<"$events_out" 2>/dev/null || echo 0)"
  idle="$(jq -s 'map(select(.type == "session.idle_killed")) | length' <<<"$events_out" 2>/dev/null || echo 0)"
  quars="$(jq -s 'map(select(.type == "session.quarantined")) | length' <<<"$events_out" 2>/dev/null || echo 0)"

  {
    echo "- Crashes: $crashes"
    echo "- Idle kills: $idle"
    echo "- Quarantines: $quars"
    echo
    echo "### By agent"
    jq -s -r '
      group_by(.payload.agent_name)
      | map({agent: .[0].payload.agent_name, n: length})
      | sort_by(-.n)
      | .[]
      | "- \(.agent): \(.n) event(s)"
    ' <<<"$events_out" 2>/dev/null || echo "_(no events)_"
    echo
    echo "### Current agent snapshot"
    gc agent list 2>/dev/null | head -50 || echo "_(gc agent list failed)_"
  } >>"$out"
}
gather_anomalies() {
  local start="$1" out="$2"

  {
    echo
    echo "## Order / hack anomalies"
    echo
  } >>"$out"

  if [[ ! -r $CITY_TOML ]]; then
    echo "_(city.toml not readable: $CITY_TOML)_" >>"$out"
    return 0
  fi
  if [[ ! -r $TRACE_FILE ]]; then
    echo "_(trace file not readable: $TRACE_FILE)_" >>"$out"
    return 0
  fi

  # Extract disabled order names from city.toml's [[orders.overrides]] blocks.
  local disabled
  disabled="$(awk '
    /^\[\[orders\.overrides\]\]/ { if (in_block && en == "false" && name != "") print name; in_block=1; name=""; en="" }
    in_block && /^[[:space:]]*name[[:space:]]*=/    { gsub(/.*=[[:space:]]*"|"[[:space:]]*$/, ""); name=$0 }
    in_block && /^[[:space:]]*enabled[[:space:]]*=/ { gsub(/.*=[[:space:]]*/, "");                 en=$0 }
    in_block && /^[[:space:]]*$/                     { if (en == "false" && name != "") print name; in_block=0 }
    END                                              { if (in_block && en == "false" && name != "") print name }
  ' "$CITY_TOML")"

  if [[ -z $disabled ]]; then
    echo "_(No overridden orders configured; no regression check possible.)_" >>"$out"
    return 0
  fi

  # Escape regex metachars in each disabled name before joining.
  local disabled_escaped
  # shellcheck disable=SC2001  # sed char-class escaping; ${//} can't do this
  disabled_escaped="$(sed 's/[].\\$*^[]/\\&/g' <<<"$disabled")"

  # Build a regex of disabled names and grep the trace for order.fired since cursor.
  local re
  re="$(tr '\n' '|' <<<"$disabled_escaped" | sed 's/|$//')"
  local fires
  fires="$(grep -E "order\\.fired subject=($re)\$" "$TRACE_FILE" 2>/dev/null || true)"

  if [[ -z $fires ]]; then
    echo "_No anomalies — overridden orders did not fire._" >>"$out"
    return 0
  fi

  {
    echo "Disabled orders that fired in the window (HACK 12 regression pattern):"
    echo
    echo "$fires" | awk '{ for (i=1; i<=NF; i++) if ($i ~ /^subject=/) { sub(/^subject=/, "", $i); print $i } }' |
      sort | uniq -c | sort -rn |
      awk '{ printf "- %s — fired %d time(s)\n", $2, $1 }'
  } >>"$out"
}

# ── polish + delivery (Tasks 7-8) ─────────────────────────────────────────
polish_with_llm() {
  local intermediate="$1"
  if ! command -v claude >/dev/null 2>&1; then
    cat "$intermediate"
    return 0
  fi

  local out
  out="$(timeout "${POLISH_TIMEOUT_SECONDS}s" claude --model "$POLISH_MODEL" -p "$POLISH_PROMPT" <"$intermediate" 2>/dev/null || true)"
  if [[ -z $out ]]; then
    cat "$intermediate"
    return 0
  fi
  printf '%s' "$out"
}
deliver() {
  local body="$1"
  local subject
  subject="Daily summary $(date '+%Y-%m-%d')"
  gc mail send "$RECIPIENT" --subject "$subject" --message "$body"
}

# ── orchestrator (Task 8) ─────────────────────────────────────────────────
main() {
  local start
  start="$(read_cursor)"
  local intermediate
  intermediate="$(mktemp -t "gc-daily-summary-XXXXXX.md")"

  {
    echo "# Gas City daily summary"
    echo
    echo "_Window: $("$DATE_BIN" -u -r "$start" +'%Y-%m-%dT%H:%M:%SZ') → $(date -u +'%Y-%m-%dT%H:%M:%SZ')_"
    echo
    # Warn if the window was clamped (cursor was older than the gap cap).
    if [[ -r $CURSOR_FILE ]]; then
      local raw
      raw="$(head -n1 "$CURSOR_FILE")"
      if [[ $raw =~ ^[0-9]+$ ]] && (($(date +%s) - raw > GAP_CAP_SECONDS)); then
        echo "> Window clamped to ${GAP_CAP_SECONDS}s (last summary epoch: $raw)"
        echo
      fi
    fi
  } >"$intermediate"

  gather_beads "$start" "$intermediate" || true
  gather_prs "$start" "$intermediate" || true
  gather_agents "$start" "$intermediate" || true
  gather_anomalies "$start" "$intermediate" || true

  local body
  body="$(polish_with_llm "$intermediate")"

  if ! deliver "$body"; then
    rm -f "$intermediate"
    return 1
  fi

  write_cursor "$(date +%s)"
  rm -f "$intermediate"
}

main "$@"
