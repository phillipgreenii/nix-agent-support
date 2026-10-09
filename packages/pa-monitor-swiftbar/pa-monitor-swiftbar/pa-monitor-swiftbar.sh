# shellcheck shell=bash
# pa-monitor-swiftbar - SwiftBar menu bar renderer for pa-monitor.
#
# Pipe-and-Filter: `pa-monitor status --json` -> one jq program -> SwiftBar
# lines on stdout. Click actions in the dropdown are direct `pa-monitor`
# invocations (explicit on/off relative to the RENDERED state, never `toggle`,
# so display lag cannot invert what the operator saw). Always exits 0: a menu
# bar plugin must never surface a raw error.
#
# Design: docs/superpowers/specs/2026-10-07-pa-monitor-swiftbar-design.md

show_help() {
  cat <<'HELP'
pa-monitor-swiftbar: Render pa-monitor's 5h usage window as SwiftBar menu bar output

Usage: pa-monitor-swiftbar [OPTIONS]

Reads `pa-monitor status --json` and prints SwiftBar plugin output. The title has
two widths, a setting that the Title row of the dropdown switches:
  wide    (the default) `5h 63% · 1h 52m` while capacity remains, `⛔ LIMIT ·
          resets 23:10 (24m)` (`⛔ 7d LIMIT · ...` for the weekly window) when a
          limit is hit, and `5h –`, `5h ?` or `5h ⚠` for an expired reading, no
          data, or a missing / unreachable pa-monitor. The dropdown has no
          duplicate detail row; its window row reads `resets 23:10 · 24m left`.
  narrow  a ONE-character title (a pie glyph for the 5h usage, or a status
          symbol) and a dropdown whose first row carries the detail (5h usage and
          time left, or the usage-limit countdown).
Either way the dropdown continues with session counts and caffeinate /
auto-resume toggle rows, then the Title row and Refresh. Colors are the same in
both widths. The setting is the file
$XDG_STATE_HOME/pa-monitor-swiftbar/title-width (default ~/.local/state), holding
`wide` or `narrow`; a missing, empty or unrecognised value reads as wide.
Always exits 0, except --set-title-width, which exits 2 on a bad value.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information
  --set-title-width MODE
                 Write the title-width setting (wide or narrow) and exit;
                 nothing is rendered. The dropdown's "Title:" row runs this.

Environment:
  PA_MONITOR_BIN             pa-monitor executable (default: pa-monitor on PATH)
  PA_SWIFTBAR_STALE_AFTER_S  Seconds after which a reading is stale (default 600)
  PA_SWIFTBAR_NOW            Epoch seconds to treat as "now" (test seam)
  PA_SWIFTBAR_TIMEOUT_S      Seconds each external call (`pa-monitor status --json`,
                             `jq`) may take before it is abandoned (default 5; a
                             non-numeric or zero value reads as 5). A timeout
                             renders the unreachable / no-data state.
  PA_SWIFTBAR_SELF           Path the Title row re-invokes (the nix plugin wrapper
                             sets a whitespace-free store path; default: $0)
HELP
}

# The whole renderer is one jq program: status JSON in, one SwiftBar line per
# output string. All time math and local clock text happen here. A parse or
# evaluation failure makes jq exit non-zero and the caller degrades to the
# "no data" state rather than showing an error.
PA_SWIFTBAR_JQ_PROGRAM=$(
  cat <<'JQ'
def num: if type == "number" then . else null end;
def ts: if type == "string" then (try fromdateiso8601 catch null) else null end;

def cd($secs):
  ($secs | floor) as $s
  | if $s >= 86400 then "\($s / 86400 | floor)d \(($s % 86400) / 3600 | floor)h"
    elif $s >= 3600 then "\($s / 3600 | floor)h \(($s % 3600) / 60 | floor)m"
    else "\($s / 60 | floor)m" end;

def mmss($secs):
  ($secs | floor) as $s
  | "\($s / 60 | floor):\($s % 60 | if . < 10 then "0\(.)" else "\(.)" end)";

# HH:MM when the instant falls on today's local date, else "Ddd HH:MM".
def clock($t):
  if ($t | strflocaltime("%Y-%m-%d")) == ($now | strflocaltime("%Y-%m-%d"))
  then $t | strflocaltime("%H:%M")
  else $t | strflocaltime("%a %H:%M") end;

def bar($pct):
  (if $pct < 0 then 0 elif $pct > 100 then 100 else $pct end) as $p
  | ($p / 100 * 18 | floor) as $filled
  | ("█" * $filled) + ("░" * (18 - $filled));

# One-character pie by floor(used): 0-12 empty, 13-37 quarter, 38-62 half,
# 63-87 three quarters, 88+ full. Callers pass an already-floored number.
def pie($used):
  if $used <= 12 then "○"
  elif $used <= 37 then "◔"
  elif $used <= 62 then "◑"
  elif $used <= 87 then "◕"
  else "●" end;

def row($text; $attrs): if $attrs == "" then $text else "\($text) | \($attrs)" end;

if type != "object" then error("status is not an object") else . end
| . as $doc
| ($doc.rate_limits // {}) as $rl
| ($rl.five_hour // {}) as $f
| ($rl.seven_day // {}) as $s
| (($f.used_pct | num) | if . == null then null else floor end) as $used
| ($f.resets_at | ts) as $reset
| ($s.used_pct | num) as $used7raw
| (if $used7raw == null then null else ($used7raw | floor) end) as $used7
| ($s.resets_at | ts) as $reset7
| ($rl.captured_at | ts) as $cap
| ($cap != null and ($now - $cap) > $stale_after) as $stale
| (if $reset == null then null else $reset - $now end) as $remaining
| (if $remaining == null or $remaining <= 0 then null
   else (((18000 - $remaining) * 100 / 18000) | floor | if . < 0 then 0 elif . > 100 then 100 else . end)
   end) as $pace
| ([($doc.sessions // [])[] | select(type == "object")]) as $sess
| ([$sess[] | select(.status == "working")] | length) as $nwork
| ([$sess[] | select(.status == "blocked")] | length) as $nblock
| ([$sess[] | select(.status == "idle")] | length) as $nidle
| ([$sess[] | select(.blocker == "usage_limit")] | length) as $nlimit

# State 2: a window at >= 100% whose reset is known and in the future.
| ([
    (if $used != null and $used >= 100 and $reset != null and $reset > $now
     then {key: "5h", label: "5h", pct: ($f.used_pct), reset: $reset} else empty end),
    (if $used7 != null and $used7 >= 100 and $reset7 != null and $reset7 > $now
     then {key: "7d", label: "7d", pct: ($s.used_pct), reset: $reset7} else empty end)
  ] | if length == 0 then null else max_by(.reset) end) as $limited

# Shared dropdown pieces.
| ("\($nwork) working · \($nblock) blocked · \($nidle) idle") as $sessionsText
| ([ (if $stale then row("reading \(($now - $cap) / 60 | floor) min old"; "color=#888888") else empty end) ]) as $ageRows
| (
    [
      (if ($doc | has("caffeinate")) and (($doc.caffeinate | type) == "object") then
         ($doc.caffeinate) as $c
         | (($c.mode) == true) as $on
         | (if ($c.process | type) == "string" then $c.process else "unknown" end) as $p
         | ((($c.grace_remaining_s | num) // 0) | mmss(.)) as $g
         | (if $on then
              ({"holding": "on (holding)", "grace": "on (grace \($g))", "off": "on (armed)", "error": "on (error)"}[$p] // "on (?)")
            else
              ({"off": "off", "holding": "off (still holding)", "grace": "off (releasing \($g))", "error": "off (error)"}[$p] // "off (?)")
            end) as $text
         | row("Caffeinate: \($text)";
               (if $on then "checked=true " else "" end)
               + "bash=\($bin) param1=caffeinate param2=\(if $on then "off" else "on" end) terminal=false refresh=true")
       else empty end),
      (if ($doc | has("auto_resume")) then
         (($doc.auto_resume) == true) as $on
         | row("Auto-resume: \(if $on then "on" else "off" end)";
               (if $on then "checked=true " else "" end)
               + "bash=\($bin) param1=auto-resume param2=\(if $on then "off" else "on" end) terminal=false refresh=true")
       else empty end)
    ]
  ) as $toggleRows
| ([ "---", $sessionsText ] + $ageRows
   + (if ($toggleRows | length) > 0 then ["---"] + $toggleRows else [] end)
   + ["---", $title_row, row("Refresh"; "refresh=true")]) as $tail
| (if $used7 != null then
     "7d: \($used7)%\(if $reset7 != null then " · resets \(clock($reset7))" else "" end)"
   else null end) as $sevenDayLine

| if $limited != null then
    # State 2: limit hit.
    ($limited.reset - $now) as $left
    | ("\(if $limited.key == "7d" then "7d " else "" end)LIMIT") as $word
    | (if $wide then
         [ row("⛔ \($word) · resets \(clock($limited.reset)) (\(cd($left)))"; "color=#cc3333"),
           "---" ]
       else
         [ row("⛔"; "color=#cc3333"),
           "---",
           row("⛔ \($limited.label) LIMIT · resets \(clock($limited.reset)) (\(cd($left)))"; "color=#cc3333") ]
       end)
      + [ "\($limited.label) window limit reached",
        "\(bar($limited.pct | num // 100)) \($limited.pct | num // 100 | floor)%",
        (if $wide then "resets \(clock($limited.reset)) · \(cd($left)) left" else "resets \(clock($limited.reset))" end),
        "\($nlimit) session\(if $nlimit == 1 then "" else "s" end) blocked on usage limit" ]
      + (if $sevenDayLine != null and $limited.key != "7d" then [$sevenDayLine] else [] end)
      + $tail
  elif $reset != null and $reset <= $now then
    # State 3: the five-hour reading belongs to a window that already rolled.
    (if $wide then
       [ row("5h –"; "color=#888888"),
         "---" ]
     else
       [ row("–"; "color=#888888"),
         "---",
         row("5h reading expired"; "color=#888888") ]
     end)
    + [ row("reading expired, waiting for next status-line capture"; "color=#888888") ]
    + $tail
  elif $used != null then
    # State 4: normal.
    (if $used >= 80 then (if $stale then "#7a2b2b" else "#cc3333" end)
     elif $pace != null and $used > $pace then (if $stale then "#8a6d00" else "#e0b000" end)
     else (if $stale then "#2a6a34" else "#3a9a4a" end) end) as $color
    | (if $wide then
         [ row("5h \($used)%\(if $remaining != null then " · \(cd($remaining))" else "" end)"; "color=\($color)"),
           "---" ]
       else
         [ row(pie($used); "color=\($color)"),
           "---",
           row("5h \($used)%\(if $remaining != null then " · \(cd($remaining)) left" else "" end)"; "color=\($color)") ]
       end)
      + [ "\(bar($f.used_pct | num)) \($used)%" ]
      + (if $sevenDayLine != null then [$sevenDayLine] else [] end)
      + (if $remaining != null then
           [(if $wide then "resets \(clock($reset)) · \(cd($remaining)) left" else "resets \(clock($reset))" end)]
         else [] end)
      + $tail
  else
    # State 5: no data.
    (if $wide then
       [ row("5h ?"; "color=#888888"),
         "---" ]
     else
       [ row("?"; "color=#888888"),
         "---",
         row("5h usage unknown"; "color=#888888") ]
     end)
    + [ row("no 5h usage reading yet"; "color=#888888") ]
    + $tail
  end
| .[]
JQ
)

# Title-width setting (bead pg2-fiw4h): `wide` (the default) or `narrow`, one
# file per plugin holding the word plus a newline. A missing, empty or
# unrecognised value reads as wide.
title_width_file() {
  printf '%s' "${XDG_STATE_HOME:-${HOME:-}/.local/state}/pa-monitor-swiftbar/title-width"
}

read_title_width() {
  local file value=""
  file=$(title_width_file)
  if [[ -r $file ]]; then
    read -r value <"$file" || true
    value=${value//[[:space:]]/}
  fi
  if [[ $value == narrow ]]; then
    printf 'narrow'
  else
    printf 'wide'
  fi
}

# --set-title-width MODE: write the setting atomically (temp file in the same
# directory, then rename) and print nothing. Exit 2 on a bad or missing MODE,
# 1 when the file cannot be written. Run by the dropdown's Title row.
set_title_width() {
  local mode=${1:-} file dir tmp
  if [[ $mode != wide && $mode != narrow ]]; then
    printf 'pa-monitor-swiftbar: --set-title-width must be wide or narrow, not: %s\n' "$mode" >&2
    return 2
  fi
  file=$(title_width_file)
  dir=$(dirname "$file")
  mkdir -p "$dir" || {
    printf 'pa-monitor-swiftbar: cannot create %s\n' "$dir" >&2
    return 1
  }
  tmp=$(mktemp "$dir/.title-width.XXXXXX") || {
    printf 'pa-monitor-swiftbar: cannot write in %s\n' "$dir" >&2
    return 1
  }
  if printf '%s\n' "$mode" >"$tmp" && mv -f "$tmp" "$file"; then
    return 0
  fi
  rm -f "$tmp"
  printf 'pa-monitor-swiftbar: cannot write %s\n' "$file" >&2
  return 1
}

# The Title row: shows the current mode and the action, and on click re-invokes
# this script to write the other mode, then refreshes. The nix plugin wrapper
# exports PA_SWIFTBAR_SELF (a whitespace-free store path: the installed plugin
# lives under "Application Support", and a space in a SwiftBar `bash=` value
# needs quoting); a bare run falls back to $0.
title_row() {
  local width=$1 other self=${PA_SWIFTBAR_SELF:-$0}
  if [[ $width == narrow ]]; then other=wide; else other=narrow; fi
  [[ $self != *[[:space:]]* ]] || self="\"$self\""
  printf 'Title: %s (click for %s) | bash=%s param1=--set-title-width param2=%s terminal=false refresh=true' \
    "$width" "$other" "$self" "$other"
}

# Message-only output for the states that cannot show data or toggles: the
# title (the caller picks the wide or narrow form), a message, the Title row
# and Refresh.
emit_message_state() {
  local title=$1 message=$2 row=$3
  printf '%s | color=#888888\n' "$title"
  printf -- '---\n'
  printf '%s | color=#888888\n' "$message"
  printf '%s\n' "$row"
  printf 'Refresh | refresh=true\n'
}

# Resolve the pa-monitor executable: $PA_MONITOR_BIN if set, else PATH.
resolve_pa_monitor() {
  local candidate
  if [[ -n ${PA_MONITOR_BIN:-} ]]; then
    [[ -x ${PA_MONITOR_BIN} ]] && printf '%s' "${PA_MONITOR_BIN}"
    return 0
  fi
  candidate=$(command -v pa-monitor 2>/dev/null || true)
  [[ -n $candidate ]] && printf '%s' "$candidate"
  return 0
}

main() {
  local bin now stale_after call_timeout status_json rc rendered width wide glyph_prefix trow

  case "${1:-}" in
  -h | --help)
    show_help
    return 0
    ;;
  esac

  now=${PA_SWIFTBAR_NOW:-}
  [[ $now =~ ^[0-9]+$ ]] || now=$(date +%s)
  stale_after=${PA_SWIFTBAR_STALE_AFTER_S:-600}
  [[ $stale_after =~ ^[0-9]+$ ]] || stale_after=600
  # Bound on each external call. A fixed 5 s is right for a menu bar refresh on
  # an idle machine but fires spuriously on a starved one (bead pg2-zacis), so
  # the tests raise it; a bad or zero value reads as the default.
  call_timeout=${PA_SWIFTBAR_TIMEOUT_S:-5}
  [[ $call_timeout =~ ^[0-9]+$ ]] && ((10#$call_timeout > 0)) || call_timeout=5

  # Title width: wide prefixes the message-state glyphs with `5h `.
  width=$(read_title_width)
  wide=false
  glyph_prefix=""
  if [[ $width == wide ]]; then
    wide=true
    glyph_prefix="5h "
  fi
  trow=$(title_row "$width")

  bin=$(resolve_pa_monitor)
  if [[ -z $bin ]]; then
    emit_message_state "${glyph_prefix}⚠" "pa-monitor not found" "$trow"
    return 0
  fi

  rc=0
  status_json=$(timeout "$call_timeout" "$bin" status --json 2>/dev/null) || rc=$?
  if ((rc == 127)); then
    emit_message_state "${glyph_prefix}⚠" "pa-monitor not found" "$trow"
    return 0
  fi
  if ((rc != 0)); then
    emit_message_state "${glyph_prefix}⚠" "pa-monitor daemon unreachable" "$trow"
    return 0
  fi

  rendered=$(printf '%s' "$status_json" | timeout "$call_timeout" jq -r \
    --argjson now "$now" --argjson stale_after "$stale_after" --arg bin "$bin" \
    --argjson wide "$wide" --arg title_row "$trow" \
    "$PA_SWIFTBAR_JQ_PROGRAM" 2>/dev/null) || rendered=""
  if [[ -z $rendered ]]; then
    # Any jq failure degrades to the no-data state (never a raw error).
    emit_message_state "${glyph_prefix}?" "no 5h usage reading yet" "$trow"
    return 0
  fi
  printf '%s\n' "$rendered"
}

# The Title row's click action: write the setting and stop (no render). The only
# path that does not exit 0, so a bad value is never silently accepted.
if [[ ${1:-} == --set-title-width ]]; then
  set_rc=0
  set_title_width "${2:-}" || set_rc=$?
  exit "$set_rc"
fi

main "$@" || true
exit 0
