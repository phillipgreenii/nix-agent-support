# shellcheck shell=bash
# Core logic for handoff-create (bead pg2-2xfbi) as testable functions, split
# out of handoff-create.sh: title normalisation, the single bd create attempt,
# the read-back (BF-4) over BOTH `bd show --json` envelope shapes, and the
# comparison against what was requested. No top-level code -- mkBashScript
# sources this file before the .sh body.
#
# Exit codes (a specific meaning is always >= 2; 1 stays the generic error):
#   2  usage / validation error -- nothing was created
#   3  bd create failed -- nothing was created
#   4  the bead was created but the read-back disagrees with what was asked

# J-1 envelope prelude (beads-lifecycle skill): `bd ... --json` returns
# {"data":[..]} only when BD_JSON_ENVELOPE=1 and a bare array otherwise; the
# build does not matter, and dispatched sessions (launchd, pg-router, nix
# sandbox) do not source the variable, so they get the bare array. Both reduce
# to ONE issue object here.
HC_ENVELOPE_PRELUDE='(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end)'

# The exact `bd create` error text for a database that does not register the
# type (verified against bd 1.2.2: "invalid issue type: handoff").
HC_UNREGISTERED_TYPE_ERROR='invalid issue type: handoff'

# hc_bd: run bd against the requested tracker root (BF-1). HC_BD_DIR empty
# means the caller's cwd.
hc_bd() {
  if [[ -n ${HC_BD_DIR:-} ]]; then
    bd -C "$HC_BD_DIR" "$@"
  else
    bd "$@"
  fi
}

# hc_normalize_title <text>: print the title with exactly one `Handoff: `
# prefix, however many the caller already wrote.
hc_normalize_title() {
  local text="$1"
  text="${text#"${text%%[![:space:]]*}"}"
  while [[ $text == Handoff:* ]]; do
    text="${text#Handoff:}"
    text="${text#"${text%%[![:space:]]*}"}"
  done
  text="${text%"${text##*[![:space:]]}"}"
  [[ -n $text ]] || return 1
  printf 'Handoff: %s' "$text"
}

# hc_build_metadata <session-id>: print the metadata JSON `bd create
# --metadata` takes (create takes JSON; `--set-metadata k=v` is the `bd
# update` spelling).
hc_build_metadata() {
  jq -cn --arg s "$1" '{handed_off_from_session: $s}'
}

# hc_create_once <type> <title> <description> <metadata> <actor> [label...]:
# run `bd create` EXACTLY ONCE. Sets HC_OUT (stdout) and HC_ERR (stderr) and
# returns bd's own exit status. The create is never chained with `||` to a
# parse step (B-7): the caller reads HC_OUT only after inspecting the status.
hc_create_once() {
  local type="$1" title="$2" desc="$3" meta="$4" actor="$5"
  shift 5
  local -a args=(create -t "$type" -p 0 --title "$title" --description "$desc" --metadata "$meta" --actor "$actor" --silent)
  local label rc out_file err_file
  for label in "$@"; do
    args+=(-l "$label")
  done
  out_file="$(mktemp)"
  err_file="$(mktemp)"
  if hc_bd "${args[@]}" >"$out_file" 2>"$err_file"; then
    rc=0
  else
    rc=$?
  fi
  HC_OUT="$(<"$out_file")"
  HC_ERR="$(<"$err_file")"
  rm -f "$out_file" "$err_file"
  return "$rc"
}

# hc_is_unregistered_type_failure: true when the last failed create (HC_ERR /
# HC_OUT) is bd rejecting the type itself, the ONE failure that earns a retry.
hc_is_unregistered_type_failure() {
  [[ $HC_ERR == *"$HC_UNREGISTERED_TYPE_ERROR"* || $HC_OUT == *"$HC_UNREGISTERED_TYPE_ERROR"* ]]
}

# hc_parse_id <create-stdout>: print the new bead id, or fail when the output
# is not exactly one id-shaped token.
hc_parse_id() {
  local out="$1"
  out="${out#"${out%%[![:space:]]*}"}"
  out="${out%"${out##*[![:space:]]}"}"
  [[ $out =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || return 1
  printf '%s' "$out"
}

# hc_normalize_show: read `bd show <id> --json` on stdin (either envelope) and
# print one compact JSON object of the fields the read-back checks.
hc_normalize_show() {
  jq -c "$HC_ENVELOPE_PRELUDE"' | {
    id: (.id // ""),
    type: (.issue_type // ""),
    title: (.title // ""),
    priority: .priority,
    first_line: ((.description // "") | split("\n") | .[0]),
    session: (.metadata.handed_off_from_session // ""),
    labels: (.labels // [])
  }'
}

# hc_check <name> <want> <got>: one read-back comparison; records a mismatch in
# the global HC_BAD.
hc_check() {
  if [[ $2 == "$3" ]]; then
    echo "handoff-create: ok: $1 = $3" >&2
  else
    echo "handoff-create: MISMATCH: $1: wanted '$2' got '$3'" >&2
    HC_BAD=1
  fi
}

# hc_readback <id> <expected-type> <title> <session-id> <attended 0|1> [label...]
# (BF-4): `bd show` the new bead and compare. Prints one `ok:`/`MISMATCH:`
# line per check on stderr. Returns 0 when every check holds, 4 otherwise.
hc_readback() {
  local id="$1" want_type="$2" want_title="$3" sid="$4" attended="$5"
  shift 5
  local show norm got want_first label
  HC_BAD=0
  if ! show="$(hc_bd show "$id" --json 2>&1)"; then
    echo "handoff-create: MISMATCH: bd show $id failed: $show" >&2
    return 4
  fi
  if ! norm="$(printf '%s' "$show" | hc_normalize_show 2>&1)"; then
    echo "handoff-create: MISMATCH: could not parse bd show $id --json: $norm" >&2
    return 4
  fi

  got="$(jq -r '.id' <<<"$norm")"
  hc_check id "$id" "$got"
  got="$(jq -r '.type' <<<"$norm")"
  hc_check type "$want_type" "$got"
  got="$(jq -r '.title' <<<"$norm")"
  hc_check title "$want_title" "$got"
  got="$(jq -r '.priority' <<<"$norm")"
  hc_check priority "0" "$got"
  got="$(jq -r '.first_line' <<<"$norm")"
  want_first="Handoff from session $sid"
  hc_check "body first line" "$want_first" "$got"
  got="$(jq -r '.session' <<<"$norm")"
  hc_check "metadata handed_off_from_session" "$sid" "$got"

  for label in "$@"; do
    if jq -e --arg l "$label" '.labels | index($l) != null' <<<"$norm" >/dev/null; then
      echo "handoff-create: ok: label $label present" >&2
    else
      echo "handoff-create: MISMATCH: label $label missing" >&2
      HC_BAD=1
    fi
  done

  if jq -e '.labels | index("human") != null' <<<"$norm" >/dev/null; then
    got=1
  else
    got=0
  fi
  hc_check "human label present (1=yes)" "$attended" "$got"

  [[ $HC_BAD -eq 0 ]] || return 4
  return 0
}
