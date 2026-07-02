# shellcheck shell=bash
# Status-line rate_limits capture (ADR 0021 §1).
#
# The status-line wrapper is the only component that sees Claude Code's raw stdin
# JSON, which carries the authoritative account-global `rate_limits` object. On each
# render this lib appends — ON CHANGE ONLY — a small allowlisted record to a JSONL
# file that lives NEXT TO the transcript (`<session_id>.status.jsonl`). pa-monitor's
# LimitsSource reader then returns the newest record across all such files.
#
# The functions here are pure logic (clamp + record building + change-compare) so
# they are unit-testable without a filesystem; capture_status_line ties them to a
# real file. The wrapper injects this lib verbatim via `builtins.readFile` and calls
# capture_status_line best-effort (`{ ... } 2>/dev/null || true`) so it can NEVER
# affect the render output or the wrapper's exit status.
#
# HARD RULES (all load-bearing, proven by ADR 0021 Phase 0):
#   * Only allowlisted fields are ever written: ts, session_id, hostname, and
#     whichever of the four rate_limits window values are PRESENT. The process env
#     (151 names incl. SSH_AUTH_SOCK) is NEVER captured generically.
#   * used_percentage is clamped/validated to [0,100] BEFORE the change-compare.
#     Claude Code bug #52326 returns an epoch-sized number for an empty window;
#     unclamped it reads as a change every render and floods the file. Out-of-range
#     or non-numeric => ABSENT (skip), never substituted with 0.
#   * Every field is independently optional. A missing value is SKIPPED, never
#     written as 0 or a 1970 timestamp. A window present one render and gone the
#     next must NOT emit a spurious change.
#   * The file is created mode 0600 (secrets adjacent; least surprise).

# clamp_pct echoes a validated used_percentage in [0,100], or nothing when the input
# is empty / non-numeric / out of range. Accepts an optional decimal fraction. The
# range gate is integer-truncated on the whole part, but the ORIGINAL value is echoed
# so downstream keeps full precision. Bug #52326's epoch value (e.g. 1782958200) is
# > 100 => absent. A leading '-' (negative) => absent.
clamp_pct() {
  local v=$1
  [ -n "$v" ] || return 0
  # Shape: pure digits with at most one decimal fraction (no sign, no extra dots).
  case $v in
  *[!0-9.]* | *.*.* | '.' | '') return 0 ;;
  esac
  # Whole part for the range gate.
  local whole=${v%%.*}
  case $whole in
  '' | *[!0-9]*) return 0 ;;
  esac
  # 10#-force base-10 so a leading zero (e.g. "08") is not read as octal.
  if [ "$((10#$whole))" -gt 100 ]; then
    return 0
  fi
  printf '%s' "$v"
}

# is_epoch echoes a positive-integer resets_at, or nothing. A resets_at is a unix
# epoch; empty / non-numeric / non-positive => absent (skip the field).
is_epoch() {
  local v=$1
  [ -n "$v" ] || return 0
  case $v in
  *[!0-9]*) return 0 ;;
  esac
  [ "$((10#$v))" -gt 0 ] || return 0
  printf '%s' "$v"
}

# json_escape emits a JSON string literal (with surrounding quotes) for an arbitrary
# value, escaping the characters JSON requires. Pure bash (no jq): session_id and
# hostname are the only inputs and are short, so the per-char scan is cheap.
json_escape() {
  local s=$1 out='"' c i
  for ((i = 0; i < ${#s}; i++)); do
    c=${s:i:1}
    case $c in
    '"') out="$out\\\"" ;;
    '\') out="$out\\\\" ;;
    $'\n') out="$out\\n" ;;
    $'\r') out="$out\\r" ;;
    $'\t') out="$out\\t" ;;
    *) out="$out$c" ;;
    esac
  done
  printf '%s"' "$out"
}

# build_status_record emits a single-line JSON object containing ONLY allowlisted
# fields. Args (positional, may be empty to mean absent):
#   $1 ts (required; the caller supplies $EPOCHSECONDS)
#   $2 session_id            $3 hostname
#   $4 five_hour_pct (raw)   $5 five_hour_resets_at (raw)
#   $6 seven_day_pct (raw)   $7 seven_day_resets_at (raw)
# Percentages are clamped and resets validated; absent fields are OMITTED entirely
# (never emitted as 0 / 1970). Returns non-zero (and prints nothing) when ts is
# empty. Keys are emitted in a stable order.
build_status_record() {
  local ts=$1 sid=$2 host=$3
  local fh_pct fh_rst sd_pct sd_rst
  fh_pct=$(clamp_pct "$4")
  fh_rst=$(is_epoch "$5")
  sd_pct=$(clamp_pct "$6")
  sd_rst=$(is_epoch "$7")

  [ -n "$ts" ] || return 1

  local out="{\"ts\":$ts"
  [ -n "$sid" ] && out="$out,\"session_id\":$(json_escape "$sid")"
  [ -n "$host" ] && out="$out,\"hostname\":$(json_escape "$host")"
  [ -n "$fh_pct" ] && out="$out,\"five_hour_pct\":$fh_pct"
  [ -n "$fh_rst" ] && out="$out,\"five_hour_resets_at\":$fh_rst"
  [ -n "$sd_pct" ] && out="$out,\"seven_day_pct\":$sd_pct"
  [ -n "$sd_rst" ] && out="$out,\"seven_day_resets_at\":$sd_rst"
  out="$out}"
  printf '%s' "$out"
}

# record_signature echoes a signature of ONLY the clamped/validated rate_limits
# values used for the change-compare — deliberately EXCLUDING ts (which changes every
# render) so the append happens only when a value actually changed. Absent fields
# contribute an empty slot, so a window disappearing is distinguishable from it moving
# to a value, and reappearing at the same value emits no spurious change.
record_signature() {
  printf '%s\x1f%s\x1f%s\x1f%s' \
    "$(clamp_pct "$1")" "$(is_epoch "$2")" "$(clamp_pct "$3")" "$(is_epoch "$4")"
}

# json_number_field plucks a numeric field's value from one build_status_record line
# (values are unquoted numbers, or absent). Pure bash; matches `"key":<number>` where
# the number ends at a comma or the closing brace.
json_number_field() {
  local line=$1 key=$2 rest
  case $line in
  *"\"$key\":"*)
    rest=${line#*\"$key\":}
    rest=${rest%%,*}
    rest=${rest%%\}*}
    printf '%s' "$rest"
    ;;
  esac
}

# last_record_signature reads the LAST line of an existing status.jsonl and echoes
# the signature of its stored values (same shape as record_signature). Empty output
# when the file is missing/empty/unparseable — which makes the first write always
# happen. jq-free: the values were written by build_status_record, so a simple field
# pluck is sufficient and avoids a jq dependency at capture time.
last_record_signature() {
  local file=$1 line="" l
  [ -r "$file" ] || return 0
  # Read the whole file, keep the last non-empty line (files are tiny, ~190 B/line).
  while IFS= read -r l || [ -n "$l" ]; do
    [ -n "$l" ] && line=$l
  done <"$file"
  [ -n "$line" ] || return 0
  printf '%s\x1f%s\x1f%s\x1f%s' \
    "$(json_number_field "$line" five_hour_pct)" \
    "$(json_number_field "$line" five_hour_resets_at)" \
    "$(json_number_field "$line" seven_day_pct)" \
    "$(json_number_field "$line" seven_day_resets_at)"
}

# capture_status_line performs the whole append-on-change capture against a real
# file. Args:
#   $1 status_file  (target <session_id>.status.jsonl path; its dir must exist)
#   $2 ts           ($EPOCHSECONDS)
#   $3 session_id   $4 hostname
#   $5 fh_pct  $6 fh_rst  $7 sd_pct  $8 sd_rst   (raw rate_limits values)
# Order (ADR 0021 §1): clamp/validate -> if all absent, skip -> compare to this
# file's last record -> append iff changed. Creates the file 0600 on first write.
# Best-effort: any failure returns non-zero WITHOUT propagating to the caller (the
# wrapper wraps the call so its exit status never affects the render).
capture_status_line() {
  local status_file=$1 ts=$2 sid=$3 host=$4
  local fh_pct=$5 fh_rst=$6 sd_pct=$7 sd_rst=$8

  local record
  record=$(build_status_record "$ts" "$sid" "$host" "$fh_pct" "$fh_rst" "$sd_pct" "$sd_rst") || return 1

  # Skip when NO rate_limits value is present (record would carry only ts/id/host).
  local sig
  sig=$(record_signature "$fh_pct" "$fh_rst" "$sd_pct" "$sd_rst")
  if [ "$sig" = $'\x1f\x1f\x1f' ]; then
    return 0
  fi

  # Change-compare against this file's last record (per-file dedup; the reader dedups
  # across files). Unchanged => no write.
  local prev
  prev=$(last_record_signature "$status_file")
  [ "$sig" = "$prev" ] && return 0

  # Create with 0600 on first write; append thereafter. The umask subshell keeps the
  # create mode tight even though >> does not re-chmod an existing file.
  if [ ! -e "$status_file" ]; then
    (
      umask 077
      : >"$status_file"
    ) || return 1
  fi
  printf '%s\n' "$record" >>"$status_file" || return 1
}
