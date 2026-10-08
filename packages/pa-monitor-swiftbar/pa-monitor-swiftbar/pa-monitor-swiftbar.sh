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

Reads `pa-monitor status --json` and prints SwiftBar plugin output: a ONE-character
title (a pie glyph for the 5h usage, or a status symbol) and a dropdown whose
first row carries the detail (5h usage and time left, or the usage-limit
countdown), followed by session counts and caffeinate / auto-resume toggle rows.
Always exits 0.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Environment:
  PA_MONITOR_BIN             pa-monitor executable (default: pa-monitor on PATH)
  PA_SWIFTBAR_STALE_AFTER_S  Seconds after which a reading is stale (default 600)
  PA_SWIFTBAR_NOW            Epoch seconds to treat as "now" (test seam)
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
   + ["---", row("Refresh"; "refresh=true")]) as $tail
| (if $used7 != null then
     "7d: \($used7)%\(if $reset7 != null then " · resets \(clock($reset7))" else "" end)"
   else null end) as $sevenDayLine

| if $limited != null then
    # State 2: limit hit.
    ($limited.reset - $now) as $left
    | [ row("⛔"; "color=#cc3333"),
        "---",
        row("⛔ \($limited.label) LIMIT · resets \(clock($limited.reset)) (\(cd($left)))"; "color=#cc3333"),
        "\($limited.label) window limit reached",
        "\(bar($limited.pct | num // 100)) \($limited.pct | num // 100 | floor)%",
        "resets \(clock($limited.reset))",
        "\($nlimit) session\(if $nlimit == 1 then "" else "s" end) blocked on usage limit" ]
      + (if $sevenDayLine != null and $limited.key != "7d" then [$sevenDayLine] else [] end)
      + $tail
  elif $reset != null and $reset <= $now then
    # State 3: the five-hour reading belongs to a window that already rolled.
    [ row("–"; "color=#888888"),
      "---",
      row("5h reading expired"; "color=#888888"),
      row("reading expired, waiting for next status-line capture"; "color=#888888") ]
    + $tail
  elif $used != null then
    # State 4: normal.
    (if $used >= 80 then (if $stale then "#7a2b2b" else "#cc3333" end)
     elif $pace != null and $used > $pace then (if $stale then "#8a6d00" else "#e0b000" end)
     else (if $stale then "#2a6a34" else "#3a9a4a" end) end) as $color
    | [ row(pie($used); "color=\($color)"),
        "---",
        row("5h \($used)%\(if $remaining != null then " · \(cd($remaining)) left" else "" end)"; "color=\($color)"),
        "\(bar($f.used_pct | num)) \($used)%" ]
      + (if $sevenDayLine != null then [$sevenDayLine] else [] end)
      + (if $remaining != null then ["resets \(clock($reset))"] else [] end)
      + $tail
  else
    # State 5: no data.
    [ row("?"; "color=#888888"),
      "---",
      row("5h usage unknown"; "color=#888888"),
      row("no 5h usage reading yet"; "color=#888888") ]
    + $tail
  end
| .[]
JQ
)

# Message-only output for the states that cannot show data or toggles.
emit_message_state() {
  local title=$1 message=$2
  printf '%s | color=#888888\n' "$title"
  printf -- '---\n'
  printf '%s | color=#888888\n' "$message"
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
  local bin now stale_after status_json rc rendered

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

  bin=$(resolve_pa_monitor)
  if [[ -z $bin ]]; then
    emit_message_state "⚠" "pa-monitor not found"
    return 0
  fi

  rc=0
  status_json=$(timeout 5 "$bin" status --json 2>/dev/null) || rc=$?
  if ((rc == 127)); then
    emit_message_state "⚠" "pa-monitor not found"
    return 0
  fi
  if ((rc != 0)); then
    emit_message_state "⚠" "pa-monitor daemon unreachable"
    return 0
  fi

  rendered=$(printf '%s' "$status_json" | timeout 5 jq -r \
    --argjson now "$now" --argjson stale_after "$stale_after" --arg bin "$bin" \
    "$PA_SWIFTBAR_JQ_PROGRAM" 2>/dev/null) || rendered=""
  if [[ -z $rendered ]]; then
    # Any jq failure degrades to the no-data state (never a raw error).
    emit_message_state "?" "no 5h usage reading yet"
    return 0
  fi
  printf '%s\n' "$rendered"
}

main "$@" || true
exit 0
