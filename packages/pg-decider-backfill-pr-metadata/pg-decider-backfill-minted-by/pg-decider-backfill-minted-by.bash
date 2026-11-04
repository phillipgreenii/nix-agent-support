# shellcheck shell=bash
# Core logic for pg-decider-backfill-minted-by (the backfill
# half of the minted-by:pg-decider stamp) as testable functions. No top-level
# code; mkBashScript sources this file before the .sh body.
#
# Exit codes: 0 ok, 1 a discovery/plan/write/read-back failure (nothing more is
# written after it), 2 usage error.

PB_LABEL="minted-by:pg-decider"

# J-1 envelope prelude: `bd ... --json` returns {"data":[..]} only when
# BD_JSON_ENVELOPE=1 and a bare array otherwise. Both reduce to ONE array.
PB_ENVELOPE_ARRAY='(if type=="object" and has("data") then .data else . end) | (if type=="array" then . else [.] end)'

# pb_bd: run bd against the requested tracker root, and ONLY that root: every
# inherited BEADS_*/BD_* variable (BEADS_DIR, BEADS_DB, server host/port, actor)
# is dropped so a stray one cannot redirect the run, backups are off, and bd is
# never allowed to spawn its own dolt server. Every call is bounded by
# `timeout` (PB_BD_TIMEOUT seconds, default 60; the listing raises it), so a hung
# bd fails that step instead of stalling the run; a re-run is safe because the
# run is idempotent.
pb_bd() {
  local -a drop=()
  local v
  for v in $(compgen -e); do
    case "$v" in
    BEADS_* | BD_*) drop+=(-u "$v") ;;
    esac
  done
  timeout "${PB_BD_TIMEOUT:-60}" env ${drop[@]+"${drop[@]}"} BEADS_DOLT_AUTO_START=0 BD_BACKUP_ENABLED=0 bd -C "$PB_BD_DIR" "$@"
}

# pb_verify_target: print the tracker bd actually resolves (`bd where`) and
# succeed only when it lies inside PB_BD_DIR.
pb_verify_target() {
  local root where
  root="$(cd "$PB_BD_DIR" 2>/dev/null && pwd -P)" || {
    echo "--bd-dir $PB_BD_DIR is not a directory" >&2
    return 1
  }
  where="$(pb_bd where 2>/dev/null | head -n 1)"
  case "$where" in
  "$root"/*) printf 'TARGET\t%s\n' "$where" ;;
  *)
    echo "bd resolved '$where', which is not inside $root" >&2
    return 1
    ;;
  esac
}

# pb_snapshot: read-only. ONE listing of every bead, any status, so planning
# works from a single consistent snapshot (a per-kind query would let the
# decider mint between two reads). Printed as the raw bd JSON. The listing of a
# large tracker takes about two minutes, so it gets its own, longer bound
# (PB_BD_LIST_TIMEOUT seconds, default 300).
pb_snapshot() {
  PB_BD_TIMEOUT="${PB_BD_LIST_TIMEOUT:-300}" pb_bd list --all -n 0 --json --brief
}

# PB_CLASSIFY is a jq program over the snapshot array. It prints one compact
# JSON object per bead of interest:
#   {id, status, title, decision, kind}
# decision is one of
#   plan      the bead is the decider's with high confidence and lacks the label
#   skip      the bead is the decider's and already carries the label
#   residual  the bead has a decider-shaped title but NO proof it is the
#             decider's (a keyless bead of the earlier tool, or a keyed-looking
#             value the decider cannot parse); reported, NEVER labelled
# Every other bead is not printed.
#
# What counts as proof (the decider's own grammar, packages/pg-decider
# internal/workitem/dedupkey.go ParseKey): a dedup_key shaped
# <type>:<ident>:<kind>[:suffix] with both leading fields non-empty and kind one
# of the six work-item kinds. The decider's escalation beads carry no
# dedup_key, so they are proven by BOTH an exact decider title (rule failure or
# PR not found) AND a parent that is such a keyed anchor.
# shellcheck disable=SC2016  # a jq program: $label and $anchors are jq variables
PB_CLASSIFY='
def kinds: ["anchor","process-feedback","review-pr","fix-ci","resolve-conflict","focus-item"];
def parts: (.metadata.dedup_key // "") | if type == "string" then split(":") else [] end;
def keyed: (parts) as $p | ($p | length) >= 3 and $p[0] != "" and $p[1] != "" and (kinds | index($p[2]) != null);
def kind_of: parts[2];
def labelled: ((.labels // []) | index($label)) != null;
def has_key: ((.metadata.dedup_key // "") | tostring) != "";
(map(select(keyed and kind_of == "anchor") | .id) | map({(.): true}) | add // {}) as $anchors
| .[]
| . as $b
| (.title // "") as $t
| (if keyed then
     {decision: (if labelled then "skip" else "plan" end), kind: kind_of}
   elif has_key then
     {decision: "residual", kind: "malformed-key"}
   elif ($t | test("^pg-decider: rule [^ ]+ keeps failing on [^ ]+ .+$")) or ($t | test("^pr not found: .+$")) then
     if ((.parent // "") != "" and ($anchors[.parent] // false)) then
       {decision: (if labelled then "skip" else "plan" end),
        kind: (if ($t | startswith("pr not found")) then "escalation-notfound" else "escalation-rule" end)}
     else
       {decision: "residual", kind: "escalation-no-anchor-parent"}
     end
   elif ($t | test("^(process-feedback|review-pr|fix-ci|resolve-conflict): [^ ]+#[0-9]+")) then
     {decision: "residual", kind: "keyless-kind-title"}
   elif ($t | test("^[^ ]+/[^ ]+#[0-9]+: ")) then
     {decision: "residual", kind: "keyless-anchor-title"}
   else empty end)
| {id: $b.id, status: ($b.status // ""), title: $t} + .
'

# pb_classify <snapshot-json>: one JSON object per bead of interest.
pb_classify() {
  jq -c --arg label "$PB_LABEL" "$PB_ENVELOPE_ARRAY"' | '"$PB_CLASSIFY" <<<"$1"
}

# pb_before <id>: the bead's current JSON (single object), either envelope.
pb_before() {
  pb_bd show "$1" --json | jq -c "$PB_ENVELOPE_ARRAY"' | .[0]'
}

# pb_write_and_verify <id>: one `bd update --add-label` (never --set-labels,
# which replaces the set), then a read-back: the label is present, every label
# the bead held before is still there, and dedup_key is unchanged. A bd that
# races another writer between the read and the update is caught by the
# superset test, not by an exact comparison.
pb_write_and_verify() {
  local id="$1" before after
  before="$(pb_before "$id")" || {
    echo "read-before failed for $id" >&2
    return 1
  }
  pb_bd update "$id" --add-label "$PB_LABEL" --actor "$PB_ACTOR" >/dev/null || {
    echo "bd update failed for $id" >&2
    return 1
  }
  after="$(pb_before "$id")" || {
    echo "read-back failed for $id" >&2
    return 1
  }
  jq -e --argjson b "$before" --arg label "$PB_LABEL" '
      ((.labels // []) | index($label)) != null
      and ((($b.labels // []) - (.labels // [])) | length) == 0
      and ((.metadata.dedup_key // null) == ($b.metadata.dedup_key // null))' \
    <<<"$after" >/dev/null || {
    echo "read-back mismatch for $id (label missing, a label was lost, or dedup_key changed)" >&2
    return 1
  }
}
