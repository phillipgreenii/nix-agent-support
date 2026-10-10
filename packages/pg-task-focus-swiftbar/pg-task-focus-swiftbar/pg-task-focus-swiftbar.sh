# shellcheck shell=bash
# pg-task-focus-swiftbar - SwiftBar streaming menu bar renderer for pg-task-focus.
#
# Pipe-and-Filter over a long-lived stream: `pg-task-focus status --watch`
# (one full state object per line) -> one jq program -> SwiftBar frames on
# stdout, separated by the streamable-plugin separator. Between state lines a
# local tick re-renders the LAST state against the clock, so the timer counts
# without a process per second (and without a request to the daemon). Click
# actions in the dropdown are direct `pg-task-focus` invocations. Always exits
# 0: a menu bar plugin must never surface a raw error.
#
# Design: docs/superpowers/specs/2026-10-07-pg-task-focus-design.md ("SwiftBar plugin")

# ---------------------------------------------------------------------------
# THE ONE SEAM for SwiftBar's streaming syntax.
#
# VERIFIED OFFLINE against the SwiftBar 2.0.1 README that ships inside the app
# (Contents/Resources/README.md, "Streamable"):
#   * the metadata tag `<swiftbar.type>streamable</swiftbar.type>` marks a
#     plugin as streamable (set in ../plugin.nix);
#   * SwiftBar runs ONE long-lived process per streamable plugin;
#   * the line `~~~` is the separator, "SwiftBar will reset the menu item on
#     each occurrence of this separator", and the README's own example has the
#     separator BETWEEN frames, none before the first or after the last.
#
# NOT VERIFIABLE WITHOUT RUNNING THE GUI (an UNVERIFIED ASSUMPTION): that a
# frame written in ONE write after a leading separator replaces the previous
# frame with no flicker, and that SwiftBar shows the last frame indefinitely
# while the process sleeps. The binary also contains an undocumented
# `useTrailingStreamSeparator` setting whose semantics the README does not
# give; this renderer does not use it. Everything that depends on the
# separator syntax lives in STREAM_SEPARATOR and emit_frame below: change only
# them if the live behavior differs.
# ---------------------------------------------------------------------------
STREAM_SEPARATOR='~~~'
FRAMES_EMITTED=0

# emit_frame FRAME: write one complete frame in ONE write, preceded by the
# separator for every frame but the first.
emit_frame() {
  local frame=$1
  if ((FRAMES_EMITTED > 0)); then
    printf '%s\n%s\n' "$STREAM_SEPARATOR" "$frame"
  else
    printf '%s\n' "$frame"
  fi
  FRAMES_EMITTED=$((FRAMES_EMITTED + 1))
}

show_help() {
  cat <<'HELP'
pg-task-focus-swiftbar: Render pg-task-focus as a streaming SwiftBar menu bar plugin

Usage: pg-task-focus-swiftbar [OPTIONS]

Without options, runs as a SwiftBar streamable plugin: keeps `pg-task-focus
status --watch` open and prints one frame per state line, separated by `~~~`,
plus a re-render of the last state every second while a cycle runs (every 15 s
otherwise). When the stream ends it prints the unreachable frame and reconnects.

The title shows the highest-priority state: READ-ONLY (a lock icon), overtime
(`Deep +04:10`), running (`Deep 12:30`), paused (`⏸ Deep`, `⏸ Deep · Resume?`),
a resume offer for another cycle (`Resume <title>?`), an ended period
(`New day: roll over`), the next due task (`Next: Post plan 22m`), then `idle`.
While a cycle runs or is paused, the next task is appended when it is overdue or
due within the due-soon window. Every lower-priority state is in the dropdown.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information
  --render       Read ONE state object from stdin (empty or invalid input renders
                 the unreachable frame), print its frame and exit. No separator.

Environment:
  PG_TASK_FOCUS_BIN                     pg-task-focus executable (default: on PATH)
  PG_TASK_FOCUS_ADDR                    daemon address, passed to every action
  PG_TASK_FOCUS_SWIFTBAR_WEB_URL        what "Open pg-task-focus" opens
                                        (default: http://$PG_TASK_FOCUS_ADDR)
  PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN   due-soon window in minutes (default 30)
  PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES  space-separated boost sizes (default "5 10 25")
  PG_TASK_FOCUS_SWIFTBAR_NOW            Epoch seconds to treat as "now" (test seam)
  PG_TASK_FOCUS_SWIFTBAR_TICK_S         seconds between ticks while a cycle runs (default 1)
  PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S    seconds between ticks otherwise (default 15)
  PG_TASK_FOCUS_SWIFTBAR_RETRY_S        seconds before reconnecting (default 5)
  PG_TASK_FOCUS_SWIFTBAR_ONCE           when 1, do not reconnect after the stream
                                        ends (test seam)
HELP
}

# The whole renderer is one jq program: state JSON in, one SwiftBar line per
# output string. All time math happens here, against $now, so the same program
# serves the first frame and every tick. A parse or evaluation failure makes jq
# exit non-zero and the caller degrades to the unreachable frame.
PG_TASK_FOCUS_JQ_PROGRAM=$(
  cat <<'JQ'
def ts: if type == "string" then (sub("\\.[0-9]+Z$"; "Z") | try fromdateiso8601 catch null) else null end;
def p2: if . < 10 then "0\(.)" else "\(.)" end;

# 12:30, or 1:02:03 from an hour up.
def clk($secs):
  ($secs | floor) as $x
  | if $x >= 3600 then "\($x / 3600 | floor):\((($x % 3600) / 60 | floor) | p2):\(($x % 60) | p2)"
    else "\($x / 60 | floor):\(($x % 60) | p2)" end;

# A length with no sign: <1m, 12m, 1h05m, 2d.
def span($secs):
  ($secs | floor | if . < 0 then -. else . end) as $a
  | if $a >= 86400 then "\($a / 86400 | floor)d"
    elif $a >= 3600 then "\($a / 3600 | floor)h\((($a % 3600) / 60 | floor) | p2)m"
    elif $a >= 60 then "\($a / 60 | floor)m"
    else "<1m" end;

# A title, type or reason can never inject a parameter, a submenu or a line.
def clean: tostring | gsub("[|\r\n]+"; " ") | gsub("\\s+"; " ") | gsub("^-+"; "") | gsub("^\\s+|\\s+$"; "");

def safe: type == "string" and test("^[A-Za-z0-9._:-]+$");
def row($text; $attrs): if $attrs == "" then $text else "\($text) | \($attrs)" end;

# A clickable row: a direct pg-task-focus invocation, naming the daemon address
# and this client explicitly (SwiftBar runs actions with its own environment,
# not the plugin's). A value that is not plainly safe, or no binary, leaves the
# text without an action rather than a malformed one.
def action($text; $args; $color):
  ((if $addr != "" then ["--addr", $addr] else [] end) + ["--client", "swiftbar"] + $args) as $all
  | if $bin == "" or ($all | all(safe) | not) then row($text; $color)
    else row($text; ([ $color,
                       "bash=\($bin)",
                       ($all | to_entries | map("param\(.key + 1)=\(.value)") | join(" ")),
                       "terminal=false refresh=false" ] | map(select(. != "")) | join(" ")))
    end;

def link($text): row($text; "href=\($web)");

def unreachable_frame:
  [ row("focus ⚠"; "color=#888888"),
    "---",
    row("pg-task-focus daemon unreachable"; "color=#888888"),
    row("Reconnecting"; "color=#888888"),
    "---",
    link("Open pg-task-focus") ];

def state_frame:
  . as $s
  | ($s.read_at | ts) as $read
  | ((if $read == null then 0 else ($now - $read) end) | if . < 0 then 0 else . end) as $age
  | (($s.store.state // "ok") == "read_only") as $ro
  | $s.focus as $focus
  | ($s.dimmed // []) as $dimmed
  | $s.resume_offer as $offer
  | $s.next as $next
  | ([($s.periods // [])[] | select(.ended == true)]) as $ended
  | ($boosts | split(" ") | map(select(test("^[0-9]+$")) | tonumber)) as $boostList

  # The focus timer, moved on by the time since the state was read.
  | (if $focus == null then null
     elif $focus.status == "running" then ($focus.remaining_seconds - $age)
     else $focus.remaining_seconds end) as $rem

  # The next task, against the clock.
  | (if $next == null then null else ($next.due | ts) end) as $due
  | (if $due == null then null else ($due - $now) end) as $dueIn
  | ($next != null and ($next.overdue == true or ($dueIn != null and $dueIn < 0))) as $overdue
  | (if $next == null then null
     elif $overdue then "\($next.title | clean) overdue \(span(if $dueIn == null then 0 else $dueIn end))"
     elif $dueIn == null then ($next.title | clean)
     else "\($next.title | clean) \(span($dueIn))" end) as $nextText
  | (if $next != null and ($overdue or ($dueIn != null and $dueIn <= $dueSoon * 60))
     then " · \($nextText)" else "" end) as $nextSuffix

  # Title, in the spec's order.
  | (if $ro then
       row(":lock.fill: READ-ONLY"; "color=#cc3333")
     elif $focus != null and $rem < 0 then
       row(":exclamationmark.triangle.fill: \($focus.type | clean) +\(clk(-$rem))\($nextSuffix)"; "color=#cc3333")
     elif $focus != null then
       row("\($focus.type | clean) \(clk($rem))\($nextSuffix)"; "")
     elif ($dimmed | length) > 0 and ($offer == null or $offer.cycle.id == $dimmed[0].id) then
       row("⏸ \($dimmed[0].type | clean)\(if $offer != null then " · Resume?" else "" end)\($nextSuffix)"; "color=#888888")
     elif $offer != null then
       row("Resume \($offer.cycle.title | clean)?"; "")
     elif ($ended | length) > 0 then
       ($ended[0].banner // "New \($ended[0].kind): roll over")
     elif $next != null then
       "Next: \($nextText)"
     else "idle" end) as $title

  # Dropdown sections.
  | ([ if $ro then
         row($s.store.reason as $r | "READ-ONLY: \($r | clean). Restart pg-task-focus to recover"; "color=#cc3333"),
         row("Actions that change anything are not offered while the daemon is read-only"; "color=#888888")
       else empty end ]) as $roSection
  | ([ if $focus != null then
         row("▶ \($focus.title | clean) · \(if $rem < 0 then "overtime +\(clk(-$rem))" else "\(clk($rem)) left" end)";
             (if $rem < 0 then "color=#cc3333" else "" end)),
         (if $ro then empty else
            action("Pause"; ["cycle", "pause", $focus.id]; ""),
            action("Stop"; ["cycle", "stop", $focus.id]; ""),
            ($boostList[] as $m | action("Boost +\($m) min"; ["cycle", "boost", $focus.id, "--minutes", "\($m)"]; ""))
          end)
       elif ($dimmed | length) == 0 then
         row("No cycle running"; "color=#888888")
       else empty end ]) as $focusSection
  | ([ $dimmed[]
       | . as $d
       | row("⏸ \($d.title | clean) · paused · \(span($d.elapsed_seconds)) elapsed"; "color=#888888"),
         (if $ro then empty else
            (if $focus == null then action("--Resume"; ["cycle", "resume", $d.id]; "")
             elif $d.can_switch == true then action("--Switch to this cycle"; ["cycle", "switch", $d.id]; "")
             else empty end),
            action("--Stop"; ["cycle", "stop", $d.id]; "")
          end) ]) as $dimmedSection
  | ([ if $offer != null then
         (if $ro then row("Resume \($offer.cycle.title | clean)?"; "color=#888888")
          else action("Resume \($offer.cycle.title | clean)?";
                      [ "cycle", (if $offer.action == "switch" then "switch" else "resume" end), $offer.cycle.id ]; "")
          end)
       else empty end ]) as $offerSection
  | ((($focus == null) and (($dimmed | length) == 0)) as $noCycle
     | [ $ended[]
         | (.banner // "New \(.kind): roll over") as $b
         | if $noCycle then link($b) else row("\($b) (stop the active cycles first)"; "color=#888888") end ]) as $endedSection
  | ([ if $s.initialized == false then
         row("Not set up yet: set your day, week and sprint in the web UI"; "color=#888888")
       elif $next != null then
         ("Next: \($next.title | clean) · due \($due | strflocaltime("%H:%M %Z")) · \(if $overdue then "overdue" else "in" end) \(span(if $dueIn == null then 0 else $dueIn end))"
          | row(.; if $overdue then "color=#cc3333" else "" end)),
         (if $ro then empty else
            action("Complete \($next.title | clean)"; ["task", "done", $next.definition]; "")
          end)
       else empty end ]) as $nextSection
  | ([ link("Open pg-task-focus") ]) as $webSection

  | [ $title ]
    + ([ $roSection, $focusSection, $dimmedSection, $offerSection, $endedSection, $nextSection, $webSection ]
       | map(select(length > 0))
       | map(["---"] + .)
       | add);

if $mode == "unreachable" or (type != "object") then unreachable_frame else state_frame end
| .[]
JQ
)

# resolve_bin: $PG_TASK_FOCUS_BIN if executable, else pg-task-focus on PATH.
resolve_bin() {
  local candidate
  if [[ -n ${PG_TASK_FOCUS_BIN:-} ]]; then
    [[ -x ${PG_TASK_FOCUS_BIN} ]] && printf '%s' "${PG_TASK_FOCUS_BIN}"
    return 0
  fi
  candidate=$(command -v pg-task-focus 2>/dev/null || true)
  [[ -n $candidate ]] && printf '%s' "$candidate"
  return 0
}

# Settings shared by every frame, read once.
BIN=""
ADDR=""
WEB_URL=""
DUE_SOON=30
BOOSTS="5 10 25"

load_settings() {
  BIN=$(resolve_bin)
  ADDR=${PG_TASK_FOCUS_ADDR:-}
  WEB_URL=${PG_TASK_FOCUS_SWIFTBAR_WEB_URL:-}
  [[ -n $WEB_URL ]] || WEB_URL="http://${ADDR:-127.0.0.1:49210}"
  DUE_SOON=${PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN:-30}
  [[ $DUE_SOON =~ ^[0-9]+$ ]] || DUE_SOON=30
  BOOSTS=${PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES:-5 10 25}
}

# now_epoch: the test seam, else the clock (a bash builtin: no process per tick).
now_epoch() {
  local n=${PG_TASK_FOCUS_SWIFTBAR_NOW:-}
  if [[ $n =~ ^[0-9]+$ ]]; then
    printf '%s' "$n"
  else
    printf '%(%s)T' -1
  fi
}

# render_frame STATE_JSON MODE: print the frame (no separator). MODE is `state`
# or `unreachable`; a state that jq cannot render degrades to unreachable.
render_frame() {
  local state=$1 mode=${2:-state} now rendered=""
  now=$(now_epoch)
  if [[ $mode == state && -n $state ]]; then
    rendered=$(printf '%s' "$state" | timeout 5 jq -r \
      --argjson now "$now" --arg bin "$BIN" --arg addr "$ADDR" --arg web "$WEB_URL" \
      --argjson dueSoon "$DUE_SOON" --arg boosts "$BOOSTS" --arg mode state \
      "$PG_TASK_FOCUS_JQ_PROGRAM" 2>/dev/null) || rendered=""
  fi
  if [[ -z $rendered ]]; then
    rendered=$(timeout 5 jq -nr \
      --argjson now "$now" --arg bin "$BIN" --arg addr "$ADDR" --arg web "$WEB_URL" \
      --argjson dueSoon "$DUE_SOON" --arg boosts "$BOOSTS" --arg mode unreachable \
      "$PG_TASK_FOCUS_JQ_PROGRAM" 2>/dev/null) || rendered=""
  fi
  # jq itself failed (not on PATH, killed): the minimal frame, never an error.
  [[ -n $rendered ]] || rendered=$'focus ⚠ | color=#888888\n---\npg-task-focus daemon unreachable'
  printf '%s' "$rendered"
}

# stream_once: one connection to `status --watch`. Prints a frame per state line
# and per tick, then returns when the stream ends.
stream_once() {
  local tick=${PG_TASK_FOCUS_SWIFTBAR_TICK_S:-1} idle_tick=${PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S:-15}
  local rfd watch_pid chunk rc buf="" last="" wait_s
  [[ $tick =~ ^[0-9]+$ ]] && ((10#$tick > 0)) || tick=1
  [[ $idle_tick =~ ^[0-9]+$ ]] && ((10#$idle_tick > 0)) || idle_tick=15

  if [[ -z $BIN ]]; then
    return 0
  fi
  # `exec` makes the substitution's PID the client's own, so it can be killed.
  exec {rfd}< <(exec "$BIN" ${ADDR:+--addr "$ADDR"} --client swiftbar status --watch 2>/dev/null)
  watch_pid=$!

  while :; do
    # A cycle that runs ticks every second; anything else only to move the
    # "in 22m" countdowns.
    if [[ $last == *'"focus":{'* ]]; then wait_s=$tick; else wait_s=$idle_tick; fi
    chunk=""
    rc=0
    read -r -t "$wait_s" -u "$rfd" chunk || rc=$?
    if ((rc == 0)); then
      last="$buf$chunk"
      buf=""
    elif ((rc > 128)); then
      # Timed out: a tick. Keep any partial line for the next read.
      buf+=$chunk
    else
      break # the stream ended
    fi
    [[ -n $last ]] || continue
    emit_frame "$(render_frame "$last" state)"
  done

  exec {rfd}<&-
  kill "$watch_pid" 2>/dev/null || true
  wait "$watch_pid" 2>/dev/null || true
  return 0
}

# stream_forever: connect, show the unreachable frame when the stream ends, wait,
# reconnect.
stream_forever() {
  local retry=${PG_TASK_FOCUS_SWIFTBAR_RETRY_S:-5}
  [[ $retry =~ ^[0-9]+$ ]] || retry=5
  while :; do
    stream_once
    emit_frame "$(render_frame "" unreachable)"
    [[ ${PG_TASK_FOCUS_SWIFTBAR_ONCE:-0} == 1 ]] && return 0
    sleep "$retry"
  done
}

main() {
  case "${1:-}" in
  -h | --help)
    show_help
    return 0
    ;;
  --render)
    local state
    state=$(cat || true)
    load_settings
    render_frame "$state" state
    printf '\n'
    return 0
    ;;
  esac
  load_settings
  stream_forever
}

main "$@" || true
exit 0
