# shellcheck shell=bash
# pg-task-focus-swiftbar - SwiftBar streaming menu bar renderer for pg-task-focus.
#
# Pipe-and-Filter over a long-lived stream: `pg-task-focus status --watch`
# (one full state object per line) -> one jq program -> SwiftBar frames on
# stdout, separated by the streamable-plugin separator. Between state lines a
# local tick re-renders the LAST state against the clock, so the timer counts
# without a process per second (and without a request to the daemon). Click
# actions in the dropdown are direct `pg-task-focus` invocations. Always exits
# 0: a menu bar plugin must never surface a raw error (except --set-title-width,
# which exits 2 on a bad value). The title has a wide and a narrow form, a
# setting re-read for every frame; see --help.
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
status --watch` open and prints one frame immediately per state line, separated
by `~~~`, plus a re-render of the last state every 5 s while a cycle runs (every
15 s otherwise). When the stream ends it prints the unreachable frame and
reconnects.

The title shows ONLY the active task, the highest-priority of: overtime
(a warning icon, `deep +<5m`), running (`deep 12m`), paused (`⏸ deep`, `⏸ deep · Resume?`), a
resume offer for another cycle (`Resume <title>?`), the next due task (`Next: Post
plan 22m`), then `idle`. While a cycle runs or is paused, the next task is
appended when it is overdue or due within the due-soon window. A read-only daemon
only prefixes a lock icon. Every alert or error state is a dropdown row, never
title text: the ended-period banner and the READ-ONLY reason are dropdown rows.

The clock is coarse, because the menu bar is refreshed every few seconds: at
least 5 min left shows whole minutes (`12m`, `5m`); under 5 min shows the next
20 s step above the time left behind a `<` (`<5m` at 299 s, `<40s` at 20 s,
`<20s` under 20 s). Overtime uses the same buckets with a `+` (`+<20s`, `+7m`).

The title has two widths, a setting the Title row of the dropdown switches:
  wide    (the default) the title described above.
  narrow  a ONE-glyph title coloured by state (green filled ring running, red
          filled ring overtime, amber ring paused, grey empty ring idle or a
          next task, orange warning unreachable, red slashed circle read-only);
          the detail the wide title would show is the first row of the dropdown.
Either way the colour of a state is the same, the Title row and Refresh end the
dropdown, and the Title row shows the current width and the one to switch to.
The setting is the file $XDG_STATE_HOME/pg-task-focus-swiftbar/title-width
(default ~/.local/state), holding `wide` or `narrow`; a missing, empty or
unrecognised value reads as wide. It is re-read for every frame, so a change shows
within a second, without a restart.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information
  --render       Read ONE state object from stdin (empty or invalid input renders
                 the unreachable frame), print its frame and exit. No separator.
  --set-title-width MODE
                 Write the title-width setting (wide or narrow) and exit; nothing
                 is rendered. Exits 2 on a bad value. The dropdown's "Title:" row
                 runs this.

Environment:
  PG_TASK_FOCUS_BIN                     pg-task-focus executable (default: on PATH)
  PG_TASK_FOCUS_ADDR                    daemon address, passed to every action
  PG_TASK_FOCUS_SWIFTBAR_WEB_URL        what "Open pg-task-focus" opens
                                        (default: http://$PG_TASK_FOCUS_ADDR)
  PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN   due-soon window in minutes (default 30)
  PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES  space-separated boost sizes (default "5 10 25")
  PG_TASK_FOCUS_SWIFTBAR_NOW            Epoch seconds to treat as "now" (test seam)
  PG_TASK_FOCUS_SWIFTBAR_SELF           path the Title row re-invokes (the nix plugin
                                        wrapper sets a whitespace-free store path;
                                        default: $0)
  PG_TASK_FOCUS_SWIFTBAR_TICK_S         seconds between ticks while a cycle runs (default 5)
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

# THE ONE CONSTANTS BLOCK for the coarse clock. A menu bar that refreshes every
# few seconds must not claim second precision, so the remaining time of a cycle
# is bucketed: at least COARSE_WHOLE_MIN_S shows whole minutes (`12m`); anything
# under it shows the NEXT multiple of COARSE_STEP_S above the remaining time,
# behind a `<` (`<5m` for 299 s, `<40s` for 20 s, `<20s` under 20 s). The bats
# tests pin each edge.
def COARSE_WHOLE_MIN_S: 300;
def COARSE_STEP_S: 20;

# A bucket bound as text: 20 -> 20s, 60 -> 1m, 280 -> 4m40s.
def bound($step):
  (($step / 60) | floor) as $m
  | ($step % 60) as $s
  | if $m == 0 then "\($s)s"
    elif $s == 0 then "\($m)m"
    else "\($m)m\($s | p2)s" end;

# The coarse text for a length of time; the sign is the caller's.
def coarse($secs):
  ($secs | floor | if . < 0 then -. else . end) as $a
  | if $a >= COARSE_WHOLE_MIN_S then "\($a / 60 | floor)m"
    else "<\(bound((($a / COARSE_STEP_S | floor) + 1) * COARSE_STEP_S))" end;

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

# THE ONE COLOUR TABLE, by state, used by the wide and the narrow title alike
# (the convention: colours are the same in both widths).
def state_color($state):
  { running:     "#3a9a4a",
    overtime:    "#cc3333",
    paused:      "#e0b000",
    idle:        "#888888",
    read_only:   "#cc3333",
    unreachable: "#e07000" }[$state];

# The narrow title: ONE glyph per state. The glyphs read as a focus target (a
# filled ring, a ring, an empty ring); only the unreachable and read-only states
# borrow a symbol. The shapes differ between states so colour is never the only
# carrier of the state.
def narrow_glyph($state):
  { running: "◉", overtime: "◉", paused: "◎", idle: "○", read_only: "⊘", unreachable: "⚠" }[$state]
  | row(.; "color=\(state_color($state))");

# The Title row: shows the current width and, on click, runs THIS script with
# --set-title-width to write the other one. No path to run (an empty $self)
# leaves the row as plain text. A path with whitespace is quoted, since
# SwiftBar splits the bash= value on it.
def title_row:
  (if $width == "narrow" then "wide" else "narrow" end) as $other
  | "Title: \($width) (click for \($other))" as $text
  | if $self == "" then row($text; "")
    else row($text; "bash=\(if ($self | test("\\s")) then "\"\($self)\"" else $self end) param1=--set-title-width param2=\($other) terminal=false refresh=true")
    end;

# The last block of every dropdown: the Title row, then Refresh.
def title_block: [ title_row, row("Refresh"; "refresh=true") ];

def unreachable_frame:
  [ (if $width == "narrow" then narrow_glyph("unreachable") else row("focus ⚠"; "color=\(state_color("unreachable"))") end),
    "---",
    row("pg-task-focus daemon unreachable"; "color=#888888"),
    row("Reconnecting"; "color=#888888"),
    "---",
    link("Open pg-task-focus"),
    "---" ] + title_block;

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

  # The title shows ONLY the active task: the running cycle, a paused one, a
  # resume offer, the next task, else idle. An alert or error state (the ended
  # period's banner, the read-only reason) is a dropdown row, never title text;
  # a read-only daemon only prefixes a lock glyph. $t carries the wide text and
  # the state that picks the colour (both widths) and the narrow glyph; nextShown
  # says whether the title already shows the next task, so the dropdown row for
  # it does not repeat the time.
  | (if $focus != null and $rem < 0 then
       { state: "overtime",
         text: ":exclamationmark.triangle.fill: \($focus.type | clean) +\(coarse(-$rem))\($nextSuffix)",
         nextShown: ($nextSuffix != "") }
     elif $focus != null then
       { state: "running", text: "\($focus.type | clean) \(coarse($rem))\($nextSuffix)", nextShown: ($nextSuffix != "") }
     elif ($dimmed | length) > 0 and ($offer == null or $offer.cycle.id == $dimmed[0].id) then
       { state: "paused",
         text: "⏸ \($dimmed[0].type | clean)\(if $offer != null then " · Resume?" else "" end)\($nextSuffix)",
         nextShown: ($nextSuffix != "") }
     elif $offer != null then
       { state: "paused", text: "Resume \($offer.cycle.title | clean)?", nextShown: false }
     elif $next != null then
       { state: "idle", text: "Next: \($nextText)", nextShown: true }
     else { state: "idle", text: "idle", nextShown: false } end) as $active
  | (if $ro then { state: "read_only", text: ":lock.fill: \($active.text)", nextShown: $active.nextShown }
     else $active end) as $t
  | row($t.text; "color=\(state_color($t.state))") as $wideTitle
  | (if $width == "narrow" then narrow_glyph($t.state) else $wideTitle end) as $title

  # Dropdown sections.
  | ([ if $ro then
         row($s.store.reason as $r | "READ-ONLY: \($r | clean). Restart pg-task-focus to recover"; "color=#cc3333"),
         row("Actions that change anything are not offered while the daemon is read-only"; "color=#888888")
       else empty end ]) as $roSection
  # The time left is in the title (wide) or the first row (narrow): the cycle
  # row names the cycle and does not repeat it.
  | ([ if $focus != null then
         row("▶ \($focus.title | clean)\(if $rem < 0 then " · overtime" else "" end)";
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
         ("Next: \($next.title | clean) · due \($due | strflocaltime("%H:%M %Z"))\(if $t.nextShown then "" else " · \(if $overdue then "overdue" else "in" end) \(span(if $dueIn == null then 0 else $dueIn end))" end)"
          | row(.; if $overdue then "color=#cc3333" else "" end)),
         (if $ro then empty else
            action("Complete \($next.title | clean)"; ["task", "done", $next.definition]; "")
          end)
       else empty end ]) as $nextSection
  | ([ link("Open pg-task-focus") ]) as $webSection

  # Narrow: the one-glyph title leaves the detail to the FIRST dropdown row.
  | ([ if $width == "narrow" then $wideTitle else empty end ]) as $detailSection

  | [ $title ]
    + ([ $detailSection, $roSection, $focusSection, $dimmedSection, $offerSection, $endedSection, $nextSection, $webSection, title_block ]
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
SELF=""
# The title width is NOT read once: this process is long-lived, so it is re-read
# for every frame (load_title_width) and a toggle shows without a restart.
TITLE_WIDTH=wide
TITLE_WIDTH_FILE=""

load_settings() {
  BIN=$(resolve_bin)
  SELF=${PG_TASK_FOCUS_SWIFTBAR_SELF:-$0}
  ADDR=${PG_TASK_FOCUS_ADDR:-}
  WEB_URL=${PG_TASK_FOCUS_SWIFTBAR_WEB_URL:-}
  [[ -n $WEB_URL ]] || WEB_URL="http://${ADDR:-127.0.0.1:49210}"
  DUE_SOON=${PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN:-30}
  [[ $DUE_SOON =~ ^[0-9]+$ ]] || DUE_SOON=30
  BOOSTS=${PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES:-5 10 25}
}

# Title-width setting: `wide` (the default) or `narrow`, one file holding the
# word plus a newline. A missing, empty or unrecognised value reads as wide.
title_width_file() {
  TITLE_WIDTH_FILE="${XDG_STATE_HOME:-${HOME:-}/.local/state}/pg-task-focus-swiftbar/title-width"
}

# load_title_width: set TITLE_WIDTH from the file. Builtins only (no process), and
# no output, so it can run at every poll and its result survives (a command
# substitution would lose the assignment).
load_title_width() {
  local file value=""
  title_width_file
  file=$TITLE_WIDTH_FILE
  if [[ -r $file ]]; then
    read -r value <"$file" || true
    value=${value//[[:space:]]/}
  fi
  if [[ $value == narrow ]]; then TITLE_WIDTH=narrow; else TITLE_WIDTH=wide; fi
}

# --set-title-width MODE: write the setting atomically (temp file in the same
# directory, then rename) and print nothing. Exit 2 on a bad or missing MODE, 1
# when the file cannot be written. Run by the dropdown's Title row.
set_title_width() {
  local mode=${1:-} file dir tmp
  if [[ $mode != wide && $mode != narrow ]]; then
    printf 'pg-task-focus-swiftbar: --set-title-width must be wide or narrow, not: %s\n' "$mode" >&2
    return 2
  fi
  title_width_file
  file=$TITLE_WIDTH_FILE
  dir=$(dirname "$file")
  mkdir -p "$dir" || {
    printf 'pg-task-focus-swiftbar: cannot create %s\n' "$dir" >&2
    return 1
  }
  tmp=$(mktemp "$dir/.title-width.XXXXXX") || {
    printf 'pg-task-focus-swiftbar: cannot write in %s\n' "$dir" >&2
    return 1
  }
  if printf '%s\n' "$mode" >"$tmp" && mv -f "$tmp" "$file"; then
    return 0
  fi
  rm -f "$tmp"
  printf 'pg-task-focus-swiftbar: cannot write %s\n' "$file" >&2
  return 1
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
      --arg width "$TITLE_WIDTH" --arg self "$SELF" \
      "$PG_TASK_FOCUS_JQ_PROGRAM" 2>/dev/null) || rendered=""
  fi
  if [[ -z $rendered ]]; then
    rendered=$(timeout 5 jq -nr \
      --argjson now "$now" --arg bin "$BIN" --arg addr "$ADDR" --arg web "$WEB_URL" \
      --argjson dueSoon "$DUE_SOON" --arg boosts "$BOOSTS" --arg mode unreachable \
      --arg width "$TITLE_WIDTH" --arg self "$SELF" \
      "$PG_TASK_FOCUS_JQ_PROGRAM" 2>/dev/null) || rendered=""
  fi
  # jq itself failed (not on PATH, killed): the minimal frame, never an error.
  if [[ -z $rendered ]]; then
    if [[ $TITLE_WIDTH == narrow ]]; then
      rendered=$'⚠ | color=#e07000\n---\npg-task-focus daemon unreachable'
    else
      rendered=$'focus ⚠ | color=#888888\n---\npg-task-focus daemon unreachable'
    fi
  fi
  printf '%s' "$rendered"
}

# stream_once: one connection to `status --watch`. Prints a frame per state line
# and per tick, then returns when the stream ends.
stream_once() {
  local tick=${PG_TASK_FOCUS_SWIFTBAR_TICK_S:-5} idle_tick=${PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S:-15}
  local rfd watch_pid chunk rc buf="" last="" wait_s now deadline fresh emitted_width
  [[ $tick =~ ^[0-9]+$ ]] && ((10#$tick > 0)) || tick=5
  [[ $idle_tick =~ ^[0-9]+$ ]] && ((10#$idle_tick > 0)) || idle_tick=15

  if [[ -z $BIN ]]; then
    return 0
  fi
  # `exec` makes the substitution's PID the client's own, so it can be killed.
  exec {rfd}< <(exec "$BIN" ${ADDR:+--addr "$ADDR"} --client swiftbar status --watch 2>/dev/null)
  watch_pid=$!

  # The menu bar needs a new frame only for a state line (at once), for a clock
  # that moved (a tick) and for a changed title width. The read therefore wakes
  # once a second, a cheap builtin poll, so a width toggle shows within a second
  # even while the timer ticks only every few seconds; a frame is emitted only
  # when one of those three happened. The tick is measured on the wall clock.
  load_title_width
  emitted_width=$TITLE_WIDTH
  printf -v now '%(%s)T' -1
  deadline=$((now + idle_tick))

  while :; do
    chunk=""
    rc=0
    fresh=0
    read -r -t 1 -u "$rfd" chunk || rc=$?
    if ((rc == 0)); then
      last="$buf$chunk"
      buf=""
      fresh=1
    elif ((rc > 128)); then
      # Timed out: keep any partial line for the next read.
      buf+=$chunk
    else
      break # the stream ended
    fi
    load_title_width
    printf -v now '%(%s)T' -1
    if ((fresh == 0)) && [[ $TITLE_WIDTH == "$emitted_width" ]] && ((now < deadline)); then
      continue
    fi
    emitted_width=$TITLE_WIDTH
    # A cycle that runs ticks every few seconds; anything else only to move the
    # "in 22m" countdowns.
    if [[ $last == *'"focus":{'* ]]; then wait_s=$tick; else wait_s=$idle_tick; fi
    deadline=$((now + wait_s))
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
    load_title_width
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
    load_title_width
    render_frame "$state" state
    printf '\n'
    return 0
    ;;
  esac
  load_settings
  stream_forever
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
