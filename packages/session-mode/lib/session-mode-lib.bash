# shellcheck shell=bash

# Shared conventions for the session-mode CLI: per-session "which loop is
# running" tracking (bead pg2-gzrn2). A record is a single JSON object,
# atomically overwritten in place (current state, not an append-only log) at
# <state_dir>/<session_id>.session-mode.json — the same directory that already
# holds <session_id>.jsonl (the transcript) and <session_id>.status.jsonl (the
# rate-limit log; ADR 0021).
#
#   { "kind": "drain-beads", "detail": "P1 only", "state": "running",
#     "started_at": "2026-09-11T14:32:00Z", "updated_at": "2026-09-11T14:40:00Z" }
#
# kind is a free-form slug (not a closed enum: a future skill needs zero
# library changes to participate). state is a closed enum: running | stopping
# | finished. detail is optional and omitted (never empty-string) when there
# is nothing to record.

# session_mode_state_dir [TRANSCRIPT_PATH] — resolve the directory a session's
# record lives in. Precedence:
#   1. $SESSION_MODE_STATE_DIR override (bats isolation; mirrors bg-tools-lib's
#      BG_DIR pattern).
#   2. dirname(TRANSCRIPT_PATH), when given — the hooks' case: hook stdin JSON
#      always carries a transcript_path.
#   3. Derived from $PWD via session_mode_encode_cwd — the three markdown
#      commands' case, which gets no stdin JSON at all, so it recreates the
#      same ~/.claude/projects/<encoded-cwd>/ directory Claude Code itself
#      writes the transcript into.
session_mode_state_dir() {
  local transcript_path="${1:-}"
  if [[ -n ${SESSION_MODE_STATE_DIR:-} ]]; then
    printf '%s' "$SESSION_MODE_STATE_DIR"
  elif [[ -n $transcript_path ]]; then
    printf '%s' "${transcript_path%/*}"
  else
    printf '%s/.claude/projects/%s' "$HOME" "$(session_mode_encode_cwd "$PWD")"
  fi
}

# session_mode_encode_cwd CWD — turn a cwd into the directory name Claude Code
# itself uses under ~/.claude/projects/: every non-alphanumeric character
# (verified empirically against real ~/.claude/projects/* entries, not
# assumed — this includes underscores and dots, not just slashes) becomes a
# single '-'. Centralized here so a corner case is a one-line fix.
session_mode_encode_cwd() {
  local cwd="$1" out="" c i
  for ((i = 0; i < ${#cwd}; i++)); do
    c=${cwd:i:1}
    case $c in
    [A-Za-z0-9]) out="$out$c" ;;
    *) out="$out-" ;;
    esac
  done
  printf '%s' "$out"
}

# session_mode_file_path DIR SESSION_ID — the record path for SESSION_ID.
session_mode_file_path() {
  printf '%s/%s.session-mode.json' "$1" "$2"
}

# session_mode_now_iso — the current UTC time, ISO-8601.
session_mode_now_iso() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

# session_mode_validate_kind KIND — shape-only slug check (lowercase letters,
# digits, hyphens; must start with a letter). Deliberately NOT a closed enum:
# a brand-new kind needs zero library changes to participate.
session_mode_validate_kind() {
  [[ $1 =~ ^[a-z][a-z0-9-]*$ ]]
}

# session_mode_validate_state STATE — closed enum: running | stopping | finished.
session_mode_validate_state() {
  case $1 in
  running | stopping | finished) return 0 ;;
  *) return 1 ;;
  esac
}

# session_mode_build_record KIND DETAIL STATE STARTED_AT UPDATED_AT
# [HANDOFF_BEAD_ID] — emit the record as a single-line JSON object via
# jq -n --arg. DETAIL is omitted entirely when empty (never written as ""),
# and is truncated to a hard cap of 40 chars with a trailing … when longer —
# the mechanical backstop; the primary defense is the caller passing an
# already-short summary (see the drain-beads.md / unblock-human-beads.md
# "Keeping detail short" instruction).
#
# HANDOFF_BEAD_ID is optional (an omitted 6th argument is treated as empty,
# via bash's "${6:-}" default) and follows the same omit-when-empty pattern
# as DETAIL — but is deliberately NOT truncated: a bead id (e.g. "tc-m08w3")
# is a fixed short format, not free text, so DETAIL's 40-char cap does not
# apply to it (bead tc-m08w3's session-wrapup "next-session handoff bead"
# field).
session_mode_build_record() {
  local kind="$1" detail="$2" state="$3" started_at="$4" updated_at="$5" handoff_bead_id="${6:-}"
  local truncated=""
  if [[ -n $detail ]]; then
    if [[ ${#detail} -gt 40 ]]; then
      truncated="${detail:0:39}…"
    else
      truncated="$detail"
    fi
  fi
  # -c (compact): the record is read back as a SINGLE line, both by
  # session_mode_read (a plain cat) and by the status-line wrapper's
  # jq-free session-mode-status.bash pattern-match plucker — jq's default
  # pretty-printer would spread it across multiple lines and insert a space
  # after each `:`, breaking both readers.
  #
  # detail and handoff_bead_id are added via `+ (if ... else {} end)` rather
  # than four separate literal-branch jq calls (one per present/absent
  # combination): both are independently optional, and the additive-merge
  # form stays correct as more optional fields are added later without the
  # branch count doubling again.
  jq -nc \
    --arg kind "$kind" \
    --arg detail "$truncated" \
    --arg state "$state" \
    --arg started_at "$started_at" \
    --arg updated_at "$updated_at" \
    --arg handoff_bead_id "$handoff_bead_id" \
    '{kind: $kind, state: $state, started_at: $started_at, updated_at: $updated_at}
     + (if $detail != "" then {detail: $detail} else {} end)
     + (if $handoff_bead_id != "" then {handoff_bead_id: $handoff_bead_id} else {} end)'
}

# session_mode_read PATH — print the record at PATH. Returns non-zero, prints
# nothing, when PATH is missing OR zero-byte (`-s` requires "exists AND is
# non-empty") — both cases must behave identically to "no record", never crash.
session_mode_read() {
  local path="$1"
  [[ -s $path ]] || return 1
  cat "$path"
}

# session_mode_write_atomic PATH JSON — write JSON to PATH via a same-directory
# mktemp + mv, so a concurrent reader (the status-line wrapper) never observes
# a half-written file (POSIX rename is atomic). Creates the directory if
# absent. Mode 0600 (this repo's convention for session-adjacent state files,
# matching capture-status.bash's status.jsonl).
session_mode_write_atomic() {
  local path="$1" json="$2" dir tmp
  dir="${path%/*}"
  mkdir -p "$dir" || return 1
  tmp="$(mktemp "$dir/.session-mode.tmp.XXXXXX")" || return 1
  chmod 0600 "$tmp" 2>/dev/null || true
  if ! printf '%s\n' "$json" >"$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  mv -f "$tmp" "$path"
}
