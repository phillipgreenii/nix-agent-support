# shellcheck shell=bash
# Core logic for pg-decider-backfill-pr-metadata (bead pg2-eclw3j.5, plan D4 of
# epic pg2-eclw3j) as testable functions. No top-level code; mkBashScript
# sources this file before the .sh body.
#
# Exit codes: 0 ok, 1 a discovery/plan/write/read-back failure (nothing more is
# written after it), 2 usage error.

# J-1 envelope prelude: `bd ... --json` returns {"data":[..]} only when
# BD_JSON_ENVELOPE=1 and a bare array otherwise. Both reduce to ONE array.
PB_ENVELOPE_ARRAY='(if type=="object" and has("data") then .data else . end) | (if type=="array" then . else [.] end)'

# pb_bd: run bd against the requested tracker root, and ONLY that root: every
# inherited BEADS_*/BD_* variable (BEADS_DIR, BEADS_DB, server host/port, actor)
# is dropped so a stray one cannot redirect the run, backups are off, and bd is
# never allowed to spawn its own dolt server.
pb_bd() {
  local -a drop=()
  local v
  for v in $(compgen -e); do
    case "$v" in
    BEADS_* | BD_*) drop+=(-u "$v") ;;
    esac
  done
  env ${drop[@]+"${drop[@]}"} BEADS_DOLT_AUTO_START=0 BD_BACKUP_ENABLED=0 bd -C "$PB_BD_DIR" "$@"
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

# pb_discover: read-only. Print one compact JSON object per decider-minted
# process-feedback child (dedup_key present and of kind process-feedback,
# parent set): {id, parent, title, metadata}.
pb_discover() {
  local out
  out="$(pb_bd list --all -n 0 --json --brief --has-metadata-key dedup_key)" || return 1
  jq -c "$PB_ENVELOPE_ARRAY"' | .[]
    | select((.parent // "") != "")
    | select((.metadata.dedup_key // "") | test(":process-feedback:"))
    | {id, parent, title: (.title // ""), metadata: (.metadata // {})}' <<<"$out"
}

# pb_parent_identity <parent-id>: set PB_P_REPO and PB_P_PR from the parent
# anchor (pr_number as plain text). Fails (message on stderr) when the parent is
# not an anchor, lacks either value, or a value has the wrong shape: repo must
# be owner/name, pr_number a positive integer with no leading zero (the decider
# normalises with Itoa, so "042" would never match).
pb_parent_identity() {
  local out
  out="$(pb_bd show "$1" --json)" || {
    echo "cannot read parent $1" >&2
    return 1
  }
  jq -e "$PB_ENVELOPE_ARRAY"' | .[0].metadata | (.dedup_key // "") | endswith(":anchor")' <<<"$out" >/dev/null || {
    echo "parent $1 is not an anchor" >&2
    return 1
  }
  PB_P_REPO="$(jq -r "$PB_ENVELOPE_ARRAY"' | .[0].metadata.repo // "" | tostring' <<<"$out")"
  PB_P_PR="$(jq -r "$PB_ENVELOPE_ARRAY"' | .[0].metadata.pr_number // "" | tostring' <<<"$out")"
  [[ $PB_P_REPO =~ ^[^/\ ]+/[^/\ ]+$ ]] || {
    echo "parent $1 has an unusable repo '$PB_P_REPO'" >&2
    return 1
  }
  [[ $PB_P_PR =~ ^[1-9][0-9]*$ ]] || {
    echo "parent $1 has an unusable pr_number '$PB_P_PR'" >&2
    return 1
  }
}

# pb_is_blank <metadata-json> <key>: true when the key is absent, null or "".
pb_is_blank() {
  jq -e --arg k "$2" '(.[$k] // "") | tostring | . == ""' <<<"$1" >/dev/null
}

# pb_old_value <metadata-json> <key>: printable current value.
pb_old_value() {
  jq -r --arg k "$2" 'if (.[$k] // "") == "" then "<absent>" else (.[$k] | tojson) end' <<<"$1"
}

# pb_write_and_verify <id> <before-metadata-json> <key> <value> [<key> <value>]...
# One merge update (--set-metadata only; --metadata would REPLACE the whole
# JSON), then a read-back: every set key must read back as the value sent and
# dedup_key must be unchanged. bd 1.3.1 coerces a numeric-looking value (a PR
# number) to a JSON NUMBER and offers no way to write a string through
# --set-metadata, so number or string both pass; the decider and the per-PR
# query accept either. Other keys are deliberately NOT compared: the decider may
# legitimately write one of them between discovery and this update.
pb_write_and_verify() {
  local id="$1" before="$2"
  shift 2
  local -a set_args=()
  local pairs="[]"
  while [[ $# -gt 0 ]]; do
    set_args+=(--set-metadata "$1=$2")
    pairs="$(jq -c --arg k "$1" --arg v "$2" '. + [[$k,$v]]' <<<"$pairs")"
    shift 2
  done
  pb_bd update "$id" "${set_args[@]}" --actor "$PB_ACTOR" >/dev/null || {
    echo "bd update failed for $id" >&2
    return 1
  }
  local after
  after="$(pb_bd show "$id" --json)" || {
    echo "read-back failed for $id" >&2
    return 1
  }
  jq -e --argjson before "$before" --argjson pairs "$pairs" "$PB_ENVELOPE_ARRAY"' | .[0].metadata as $a
    | ($pairs | all(. as [$k,$v] | (($a[$k] | type) == "string" or ($a[$k] | type) == "number") and ($a[$k] | tostring) == $v))
      and ($a.dedup_key == $before.dedup_key)' \
    <<<"$after" >/dev/null || {
    echo "read-back mismatch for $id (set keys differ from what was sent, or dedup_key changed)" >&2
    return 1
  }
}
