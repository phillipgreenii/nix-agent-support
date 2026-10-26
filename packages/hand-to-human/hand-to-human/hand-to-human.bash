# shellcheck shell=bash
# Core logic for hand-to-human (bead pg2-ikkpk) as testable functions, split out
# of hand-to-human.sh: the three canonical label names, the ONE update call
# (bd or pg-connector), and the read-back (BF-4) over both `bd show --json`
# envelope shapes. No top-level code -- mkBashScript sources this file before
# the .sh body.
#
# The vocabulary (which label means what, and when to write it) is defined ONCE
# in docs/guides/bead-label-vocabulary.md and ADR 0105. The names below are the
# single WRITE label per role named there; they MUST NOT be hand-written by any
# other agent code path.
#
# Exit codes (a specific meaning is always >= 2; 1 stays the generic error):
#   2  usage / validation error -- nothing was written
#   3  the update call failed -- nothing was written
#   4  the update succeeded, but the read-back disagrees
#   5  the claim guard (--if-assignee / --if-status) did not match: bd exited 13
#      and wrote nothing (someone else got there first)

HTH_HUMAN_LABEL=human
HTH_ATTENTION_LABEL=attention
HTH_ESCALATED_LABEL=escalated

# Optional extras of the ONE update call, set by hand-to-human.sh (bead
# pg2-8num0): HTH_ALSO_ADD is the extra labels added next to human + attention
# (--also-add; already validated and de-duplicated); HTH_NOTE is the text
# appended to the bead's notes (--note, bd path only). Both empty means the plain
# form, whose argv is unchanged.
HTH_ALSO_ADD=()
HTH_NOTE=""

# Optional claim guard of the ONE bd update (bead pg2-f9x7i): bd's own
# `--if-assignee VALUE` / `--if-status VALUE` make the update a no-op that exits
# 13 when the bead's current assignee/status differs. An empty assignee is a
# legitimate guard value ("unassigned"), so presence is tracked separately.
HTH_IF_ASSIGNEE_SET=0
HTH_IF_ASSIGNEE=""
HTH_IF_STATUS=""
# bd's exit status for a guard mismatch (nothing written).
# shellcheck disable=SC2034  # read by hand-to-human.sh after hth_run_bd
HTH_BD_GUARD_MISMATCH_RC=13

# J-1 envelope prelude (beads-lifecycle skill): `bd ... --json` returns
# {"data":[..]} only when BD_JSON_ENVELOPE=1 and a bare array otherwise.
HTH_ENVELOPE_PRELUDE='(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end)'

# hth_bd: run bd against the requested tracker root (BF-1). HTH_BD_DIR empty
# means the caller's cwd.
hth_bd() {
  if [[ -n ${HTH_BD_DIR:-} ]]; then
    bd -C "$HTH_BD_DIR" "$@"
  else
    bd "$@"
  fi
}

# hth_remove_labels [extra...]: print the labels the one call removes, one per
# line: escalated first, then each extra (de-duplicated, never a label the call
# adds).
hth_remove_labels() {
  local -a seen=("$HTH_ESCALATED_LABEL")
  local l s dup
  printf '%s\n' "$HTH_ESCALATED_LABEL"
  for l in "$@"; do
    dup=0
    for s in "${seen[@]}"; do
      [[ $s == "$l" ]] && dup=1
    done
    if [[ $dup -eq 0 ]]; then
      seen+=("$l")
      printf '%s\n' "$l"
    fi
  done
}

# hth_update_args <actor> <remove-label...>: print the argv (one element per
# line, WITHOUT the `bd`/`pg-connector issue` prefix and WITHOUT the id) of the
# single update call. The adds (human, attention, then each HTH_ALSO_ADD label)
# come first and are in the SAME call as the removes. The note (HTH_NOTE) is NOT
# printed here: it can span lines, which this one-element-per-line format cannot
# carry, so hth_run_bd appends `--append-notes` itself. The adds precede the
# removes because a bead carrying neither the escalated nor the human labels is
# visible to the normal work queues, so the human labels MUST be on the bead no
# later than the escalated removal (LV-4; beads-lifecycle D-5 reasoning).
hth_update_args() {
  local actor="$1" l
  shift
  printf '%s\n' --add-label "$HTH_HUMAN_LABEL" --add-label "$HTH_ATTENTION_LABEL"
  for l in ${HTH_ALSO_ADD[@]+"${HTH_ALSO_ADD[@]}"}; do
    printf '%s\n' --add-label "$l"
  done
  for l in "$@"; do
    printf '%s\n' --remove-label "$l"
  done
  if [[ -n $actor ]]; then
    printf '%s\n' --actor "$actor"
  fi
}

# hth_run_bd <id> <actor> <remove-label...>: the ONE bd update. Sets HTH_OUT /
# HTH_ERR and returns bd's own exit status. Never chained with `||` to a parse
# step (B-7).
hth_run_bd() {
  local id="$1" actor="$2" rc out_file err_file
  shift 2
  local -a args=()
  mapfile -t args < <(hth_update_args "$actor" "$@")
  if [[ -n $HTH_NOTE ]]; then
    args+=(--append-notes "$HTH_NOTE")
  fi
  if [[ $HTH_IF_ASSIGNEE_SET -eq 1 ]]; then
    args+=(--if-assignee "$HTH_IF_ASSIGNEE")
  fi
  if [[ -n $HTH_IF_STATUS ]]; then
    args+=(--if-status "$HTH_IF_STATUS")
  fi
  out_file="$(mktemp)"
  err_file="$(mktemp)"
  if hth_bd update "${args[@]}" -- "$id" >"$out_file" 2>"$err_file"; then
    rc=0
  else
    rc=$?
  fi
  # shellcheck disable=SC2034  # read by hand-to-human.sh after the call
  HTH_OUT="$(<"$out_file")"
  # shellcheck disable=SC2034  # read by hand-to-human.sh after the call
  HTH_ERR="$(<"$err_file")"
  rm -f "$out_file" "$err_file"
  return "$rc"
}

# hth_run_connector <backend> <id> <remove-label...>: the ONE
# `pg-connector issue update` for a session that reaches the tracker through a
# pinned pg-connector backend (the escalation-triager). pg-connector applies
# every populated field of an update in one backend call, so the adds and the
# removal stay one write. pg-connector has no actor flag (the backend instance
# carries identity), so none is passed.
hth_run_connector() {
  local backend="$1" id="$2" rc out_file err_file l
  shift 2
  local -a args=(issue update "$id" --backend "$backend" --add-label "$HTH_HUMAN_LABEL" --add-label "$HTH_ATTENTION_LABEL")
  for l in ${HTH_ALSO_ADD[@]+"${HTH_ALSO_ADD[@]}"}; do
    args+=(--add-label "$l")
  done
  for l in "$@"; do
    args+=(--remove-label "$l")
  done
  out_file="$(mktemp)"
  err_file="$(mktemp)"
  if pg-connector "${args[@]}" >"$out_file" 2>"$err_file"; then
    rc=0
  else
    rc=$?
  fi
  # shellcheck disable=SC2034  # read by hand-to-human.sh after the call
  HTH_OUT="$(<"$out_file")"
  # shellcheck disable=SC2034  # read by hand-to-human.sh after the call
  HTH_ERR="$(<"$err_file")"
  rm -f "$out_file" "$err_file"
  return "$rc"
}

# hth_readback <id> <remove-label...> (BF-4): `bd show` the bead and check that
# human, attention and every HTH_ALSO_ADD label are present, every removed label
# is absent, and (when HTH_NOTE is set) the note text is in the bead's notes. Prints one
# `ok:`/`MISMATCH:` line per check on stderr. Returns 0 when every check holds,
# 4 otherwise.
hth_readback() {
  local id="$1" show labels l bad=0 notes
  shift
  if ! show="$(hth_bd show "$id" --json 2>&1)"; then
    echo "hand-to-human: MISMATCH: bd show $id failed: $show" >&2
    return 4
  fi
  if ! labels="$(printf '%s' "$show" | jq -c "$HTH_ENVELOPE_PRELUDE"' | (.labels // [])' 2>&1)"; then
    echo "hand-to-human: MISMATCH: could not parse bd show $id --json: $labels" >&2
    return 4
  fi
  for l in "$HTH_HUMAN_LABEL" "$HTH_ATTENTION_LABEL" ${HTH_ALSO_ADD[@]+"${HTH_ALSO_ADD[@]}"}; do
    if jq -e --arg l "$l" 'index($l) != null' <<<"$labels" >/dev/null; then
      echo "hand-to-human: ok: label $l present" >&2
    else
      echo "hand-to-human: MISMATCH: label $l missing (labels: $labels)" >&2
      bad=1
    fi
  done
  for l in "$@"; do
    if jq -e --arg l "$l" 'index($l) == null' <<<"$labels" >/dev/null; then
      echo "hand-to-human: ok: label $l absent" >&2
    else
      echo "hand-to-human: MISMATCH: label $l still present (labels: $labels)" >&2
      bad=1
    fi
  done
  if [[ -n $HTH_NOTE ]]; then
    if ! notes="$(printf '%s' "$show" | jq -r "$HTH_ENVELOPE_PRELUDE"' | (.notes // "")' 2>&1)"; then
      echo "hand-to-human: MISMATCH: could not read notes of bd show $id --json: $notes" >&2
      return 4
    fi
    if [[ $notes == *"$HTH_NOTE"* ]]; then
      echo "hand-to-human: ok: note present" >&2
    else
      echo "hand-to-human: MISMATCH: note missing from the bead's notes" >&2
      bad=1
    fi
  fi
  [[ $bad -eq 0 ]] || return 4
}
