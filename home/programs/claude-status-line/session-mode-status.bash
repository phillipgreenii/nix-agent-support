# shellcheck shell=bash
# Status-line session-mode read (bead pg2-gzrn2).
#
# The status-line wrapper is injected verbatim via `builtins.readFile` (see
# scripts.nix), exactly like capture-status.bash/strip-ansi.bash, and this file
# is deliberately jq-free: the wrapper's own single-jq-call-per-render
# discipline (see .claude/rules/claude-status-line.md) means every OTHER value
# it needs from a file (not Claude's own stdin JSON) is plucked with a pure
# bash pattern match, never a second jq subprocess. The record the
# session-mode CLI WRITES still uses jq (session_mode_build_record, in
# packages/session-mode/lib/session-mode-lib.bash) — that is a different
# component, off the render hot path.
#
# json_string_field mirrors capture-status.bash's json_number_field, adapted
# for a QUOTED string value: it decodes the two common JSON escapes (\" and
# \\) so kind/state/detail (all controlled slugs with no such risk EXCEPT
# detail, which echoes arbitrary $ARGUMENTS text) round-trip correctly for the
# common case. A rarer escape (\n, \t, a unicode \uXXXX sequence) in detail is
# a cosmetic display-only imperfection — the record file itself stays correct
# — never a correctness bug (accepted risk, see the design's "Flagged,
# accepted risk" note).

# json_string_field LINE KEY — pluck the decoded value of a `"KEY":"..."`
# field from one compact single-line JSON object. Empty output (and a
# non-zero exit via the `case` falling through) when KEY is absent.
json_string_field() {
  local line=$1 key=$2 rest
  case $line in
  *"\"$key\":\""*)
    rest=${line#*\"$key\":\"}
    local raw="" i=0 n=${#rest} c nextc
    while ((i < n)); do
      c=${rest:i:1}
      if [[ $c == '\' ]]; then
        nextc=${rest:i+1:1}
        case $nextc in
        '"')
          raw="$raw\""
          i=$((i + 2))
          ;;
        '\')
          raw="$raw\\"
          i=$((i + 2))
          ;;
        *)
          raw="$raw$c"
          i=$((i + 1))
          ;;
        esac
      elif [[ $c == '"' ]]; then
        break
      else
        raw="$raw$c"
        i=$((i + 1))
      fi
    done
    printf '%s' "$raw"
    ;;
  esac
}

# read_session_mode PATH — read the one-line record at PATH (if any) and
# assign CLAUDE_SL_SESSION_MODE_KIND / _STATE / _DETAIL directly (each reset
# to empty first, so a caller sees a clean absent-vs-present signal regardless
# of what a previous call assigned — no eval "$(jq ...)" needed here, since
# there is nothing to shell-quote-and-eval). Tolerates a
# missing/unreadable/empty file: assigns nothing but empty, never errors.
read_session_mode() {
  local path=$1 line=""
  CLAUDE_SL_SESSION_MODE_KIND=""
  CLAUDE_SL_SESSION_MODE_STATE=""
  CLAUDE_SL_SESSION_MODE_DETAIL=""
  [[ -r $path ]] || return 0
  IFS= read -r line <"$path" || true
  [[ -n $line ]] || return 0
  # shellcheck disable=SC2034 # consumed externally: exported by the wrapper (scripts.nix's mkWrapperScript) and read by sessionModePart
  CLAUDE_SL_SESSION_MODE_KIND=$(json_string_field "$line" kind)
  # shellcheck disable=SC2034 # consumed externally: exported by the wrapper (scripts.nix's mkWrapperScript) and read by sessionModePart
  CLAUDE_SL_SESSION_MODE_STATE=$(json_string_field "$line" state)
  # shellcheck disable=SC2034 # consumed externally: exported by the wrapper (scripts.nix's mkWrapperScript) and read by sessionModePart
  CLAUDE_SL_SESSION_MODE_DETAIL=$(json_string_field "$line" detail)
}
