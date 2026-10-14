# shellcheck shell=bash
# Status-line compaction marker read (bead pg2-thkh0.4, plan section 2.2).
#
# The status-line wrapper injects this file verbatim via `builtins.readFile` (see
# scripts.nix), exactly like session-mode-status.bash. It is deliberately jq-free:
# the wrapper's one-jq-call-per-render discipline (see
# .claude/rules/claude-status-line.md) means any OTHER value read from a file is a
# pure-bash pluck of one tiny sibling file, never a second jq subprocess. The marker
# itself is WRITTEN by `session-mode hook post-compact` (packages/session-mode),
# which does use jq; that is a different component, off the render hot path.
#
# Marker contract: `<transcript_dir>/<session_id>.compaction.json`, one compact
# single-line JSON object `{"ts":<epoch seconds>,"trigger":"manual|auto|unknown"}`.
#
# Every helper uses `local`: this file is inlined at the wrapper's top level, whose
# names (d, gitdir, hl, gl, out, parts, line, lines, cur, seg, ...) would otherwise be
# clobbered. It defines its own plucks because capture-status.bash (home of
# json_number_field) is injected LATER in the wrapper.

# compaction_pluck_ts LINE — print the value of the `"ts":` field when it is a
# positive integer (at most 15 digits, so the later arithmetic cannot overflow);
# print nothing for an absent, non-numeric, zero, negative or fractional value
# (cf. ADR 0021 on bug #52326: values are validated, never trusted).
compaction_pluck_ts() {
  local line=$1 rest
  case $line in
  *'"ts":'*)
    rest=${line#*\"ts\":}
    rest=${rest%%,*}
    rest=${rest%%\}*}
    rest=${rest//[[:space:]]/}
    if [[ $rest =~ ^[1-9][0-9]{0,14}$ ]]; then
      printf '%s' "$rest"
    fi
    ;;
  esac
}

# compaction_pluck_trigger LINE — print the `"trigger":"..."` value when it is one of
# manual|auto|unknown; print nothing otherwise.
compaction_pluck_trigger() {
  local line=$1 rest
  case $line in
  *'"trigger":"'*)
    rest=${line#*\"trigger\":\"}
    rest=${rest%%\"*}
    case $rest in
    manual | auto | unknown) printf '%s' "$rest" ;;
    esac
    ;;
  esac
}

# read_compaction_marker PATH NOW — read the one-line marker at PATH and assign
# CLAUDE_SL_COMPACT_AGE (whole seconds since the compaction, NOW - ts, clamped to 0
# when the clock is skewed backwards) and CLAUDE_SL_COMPACT_TRIGGER (unused by the
# display in v1). Both are reset to empty first, and stay empty for a missing,
# unreadable, zero-byte or malformed file, an invalid `ts`, or a non-integer NOW —
# never an error. NOW is an argument (not $EPOCHSECONDS) so tests can pin the clock.
read_compaction_marker() {
  local path=$1 now=$2 line="" ts
  CLAUDE_SL_COMPACT_AGE=""
  CLAUDE_SL_COMPACT_TRIGGER=""
  [[ $now =~ ^[0-9]{1,15}$ ]] || return 0
  [[ -r $path ]] || return 0
  IFS= read -r line <"$path" || true
  [[ -n $line ]] || return 0
  ts=$(compaction_pluck_ts "$line")
  [[ -n $ts ]] || return 0
  # 10# forces base 10 (a leading zero is already excluded by the pluck, NOW is not).
  # shellcheck disable=SC2034 # consumed externally: exported by the wrapper (scripts.nix's mkWrapperScript) and read by contextPart
  CLAUDE_SL_COMPACT_AGE=$((10#$now - ts))
  if ((CLAUDE_SL_COMPACT_AGE < 0)); then
    CLAUDE_SL_COMPACT_AGE=0
  fi
  # shellcheck disable=SC2034 # consumed externally: reserved for future display use
  CLAUDE_SL_COMPACT_TRIGGER=$(compaction_pluck_trigger "$line")
}
