#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level (subprocess) tests for the pa-monitor-swiftbar renderer. A stub
# `pa-monitor` on PA_MONITOR_BIN prints fixture JSON; PA_SWIFTBAR_NOW and TZ are
# pinned so every countdown and clock string is deterministic. TZ uses a POSIX
# fixed-offset string (EST5 = UTC-5, no DST) so no tzdata is needed in a sandbox.

setup() {
  TEST_DIR="$(mktemp -d)"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  # Never touch the real ~/.local/state: every test gets its own state root.
  # The title width defaults to wide in production, but most of this suite was
  # written against the narrow shape, so every test starts in narrow mode; the
  # title-width tests at the end set the mode they mean (or remove the file).
  export XDG_STATE_HOME="$TEST_DIR/xdg-state"
  TITLE_WIDTH_FILE="$XDG_STATE_HOME/pa-monitor-swiftbar/title-width"
  set_width narrow

  local scripts_dir="${SCRIPTS_DIR:-}"
  if [[ -z $scripts_dir ]]; then
    scripts_dir="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  # Prefer the assembled artifact (strict mode, runtimeDeps wrapper) when the
  # nix check provides it; fall back to the raw source for a local `bats tests/`.
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    PLUGIN=("$SCRIPT_UNDER_TEST")
  else
    PLUGIN=(bash "$scripts_dir/pa-monitor-swiftbar.sh")
  fi

  # NOW = 2026-10-08T03:46:00Z = 2026-10-07 22:46 local (EST5).
  NOW="$(jq -n '"2026-10-08T03:46:00Z" | fromdateiso8601')"
  export TZ=EST5

  STUB="$TEST_DIR/pa-monitor"
  cat >"$STUB" <<STUBEOF
#!$(command -v bash)
if [ "\${1:-}" = "status" ] && [ "\${2:-}" = "--json" ]; then
  cat "$TEST_DIR/status.json"
  exit "\${STUB_RC:-0}"
fi
exit 64
STUBEOF
  chmod +x "$STUB"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# set_width MODE -> write the setting file exactly as the plugin does.
set_width() {
  mkdir -p "$(dirname "$TITLE_WIDTH_FILE")"
  printf '%s\n' "$1" >"$TITLE_WIDTH_FILE"
}

# at OFFSET -> ISO-8601 UTC instant NOW+OFFSET seconds.
at() {
  jq -rn --argjson t "$((NOW + $1))" '$t | todate'
}

# status FILTER -> writes the status fixture: a base document (no limits, two
# toggles off, captured one minute ago) with FILTER applied.
status() {
  jq -n "{
    sessions: [],
    rate_limits: {captured_at: \"$(at -60)\"},
    caffeinate: {mode: false, process: \"off\"},
    auto_resume: false
  } | $1" >"$TEST_DIR/status.json"
}

# plugin -> runs the renderer with the pinned environment.
plugin() {
  run env PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
}

# title -> first output line.
title() { printf '%s\n' "$output" | head -n1; }

# row1 -> the first dropdown row (the line right after the first `---`).
row1() { printf '%s\n' "$output" | sed -n '3p'; }

# glyph -> the title text before " | ".
glyph() { title | sed 's/ | .*$//'; }

# charcount TEXT -> number of Unicode characters (code points) in TEXT. jq
# counts code points regardless of the locale, which `${#var}` does not.
charcount() { printf '%s' "$1" | jq -Rr 'length'; }

# assert_one_char -> the title text before " | " is exactly one character.
assert_one_char() {
  [ "$(charcount "$(glyph)")" -eq 1 ]
}

# used_at PCT -> renders a normal (state 4) reading at PCT percent used with 1h52m left.
used_at() {
  status ".rate_limits.five_hour = {used_pct: $1, resets_at: \"$(at 6720)\"}"
  plugin
}

# --- help / invariants ------------------------------------------------------

@test "--help prints usage and exits 0" {
  run "${PLUGIN[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pa-monitor-swiftbar"* ]]
}

@test "always exits 0 and never prints an error" {
  printf 'not json at all' >"$TEST_DIR/status.json"
  plugin
  [ "$status" -eq 0 ]
  [[ $output != *"parse error"* ]]
  [[ $output != *"jq:"* ]]
}

# --- state 4: normal, glyph and colors --------------------------------------

@test "green: below pace shows the pie glyph; the details are the first dropdown row" {
  # 63% used, 1h52m (6720s) left -> pace = (18000-6720)*100/18000 = 62; 63 > 62
  # would be yellow, so use 50% for green.
  status ".rate_limits.five_hour = {used_pct: 50.9, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$status" -eq 0 ]
  [ "$(title)" = "◑ | color=#3a9a4a" ]
  [ "$(row1)" = "5h 50% · 1h 52m left | color=#3a9a4a" ]
}

@test "yellow: used exceeds pace" {
  status ".rate_limits.five_hour = {used_pct: 63.4, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "◕ | color=#e0b000" ]
  [ "$(row1)" = "5h 63% · 1h 52m left | color=#e0b000" ]
}

@test "pace boundary: used equal to pace is green, one above is yellow" {
  # remaining 6720 -> pace 62.
  used_at 62
  [ "$(title)" = "◑ | color=#3a9a4a" ]
  [ "$(row1)" = "5h 62% · 1h 52m left | color=#3a9a4a" ]
  used_at 63
  [ "$(title)" = "◕ | color=#e0b000" ]
  [ "$(row1)" = "5h 63% · 1h 52m left | color=#e0b000" ]
}

@test "red: used at exactly 80 is red, 79 is not" {
  used_at 80
  [ "$(title)" = "◕ | color=#cc3333" ]
  [ "$(row1)" = "5h 80% · 1h 52m left | color=#cc3333" ]
  used_at 79.99
  [ "$(title)" = "◕ | color=#e0b000" ]
  [[ $(row1) == *"color=#e0b000" ]]
}

@test "pace is clamped: reset farther than 5h away never yellows" {
  # remaining 20000 > 18000 -> negative raw pace clamps to 0; used 5 > 0 -> yellow
  status ".rate_limits.five_hour = {used_pct: 5, resets_at: \"$(at 20000)\"}"
  plugin
  [ "$(title)" = "○ | color=#e0b000" ]
  [[ $(row1) == "5h 5% · 5h 33m left"* ]]
  [[ $(row1) == *"color=#e0b000" ]]
}

@test "stale variants: dimmed red, yellow and green keep the glyph and dim the color" {
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 90, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "● | color=#7a2b2b" ]
  [[ $(row1) == *"color=#7a2b2b" ]]
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 63, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "◕ | color=#8a6d00" ]
  [[ $(row1) == *"color=#8a6d00" ]]
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "○ | color=#2a6a34" ]
  [[ $(row1) == *"color=#2a6a34" ]]
}

@test "stale boundary: exactly at the threshold is still fresh" {
  status ".rate_limits.captured_at = \"$(at -600)\" | .rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  plugin
  [[ $(title) == *"color=#3a9a4a" ]]
}

@test "PA_SWIFTBAR_STALE_AFTER_S overrides the stale threshold" {
  status ".rate_limits.captured_at = \"$(at -120)\" | .rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  run env PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" PA_SWIFTBAR_STALE_AFTER_S=60 "${PLUGIN[@]}"
  [[ $(title) == *"color=#2a6a34" ]]
  [[ $output == *"reading 2 min old"* ]]
}

@test "data-age row appears only when stale" {
  status ".rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  plugin
  [[ $output != *"min old"* ]]
  status ".rate_limits.captured_at = \"$(at -840)\" | .rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  plugin
  [[ $output == *"reading 14 min old"* ]]
}

@test "unknown resets_at: percent only in the first row, no countdown, no pace-based yellow" {
  status ".rate_limits.five_hour = {used_pct: 63}"
  plugin
  [ "$(title)" = "◕ | color=#3a9a4a" ]
  [ "$(row1)" = "5h 63% | color=#3a9a4a" ]
  [[ $output != *"left"* ]]
}

@test "used 100 with absent resets_at renders a red full pie, first row 5h 100% with no countdown" {
  status ".rate_limits.five_hour = {used_pct: 100}"
  plugin
  [ "$(title)" = "● | color=#cc3333" ]
  [ "$(row1)" = "5h 100% | color=#cc3333" ]
}

# --- pie glyph boundaries ----------------------------------------------------

@test "pie glyph follows floor(used) at every bucket boundary" {
  local pair pct want
  for pair in 0:○ 12:○ 13:◔ 37:◔ 38:◑ 62:◑ 63:◕ 87:◕ 88:●; do
    pct=${pair%%:*}
    want=${pair#*:}
    used_at "$pct"
    [ "$(glyph)" = "$want" ] || {
      echo "used=$pct: got '$(glyph)', want '$want'" >&2
      return 1
    }
  done
}

@test "pie glyph at 100 percent needs an unknown reset (a known future reset is the limit state)" {
  status ".rate_limits.five_hour = {used_pct: 100}"
  plugin
  [ "$(glyph)" = "●" ]
}

@test "pie glyph uses floor, not rounding: 12.9 stays in the first bucket, 37.9 in the second" {
  used_at 12.9
  [ "$(glyph)" = "○" ]
  used_at 37.9
  [ "$(glyph)" = "◔" ]
  used_at 87.9
  [ "$(glyph)" = "◕" ]
}

@test "pie glyph clamps out-of-range usage" {
  status ".rate_limits.five_hour = {used_pct: 250}"
  plugin
  [ "$(glyph)" = "●" ]
  used_at -3
  [ "$(glyph)" = "○" ]
}

# --- one-character title -----------------------------------------------------

@test "every title line is exactly one character before the color parameter" {
  # state 4 (each bucket, fresh and stale)
  for pct in 0 20 50 70 95; do
    used_at "$pct"
    assert_one_char
  done
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 70, resets_at: \"$(at 6720)\"}"
  plugin
  assert_one_char
  # state 4 without a reset
  status ".rate_limits.five_hour = {used_pct: 63}"
  plugin
  assert_one_char
  # state 2 (5h and 7d)
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  assert_one_char
  status ".rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 273600)\"}"
  plugin
  assert_one_char
  # state 3
  status ".rate_limits.five_hour = {used_pct: 30, resets_at: \"$(at -10)\"}"
  plugin
  assert_one_char
  # state 5
  status "del(.rate_limits)"
  plugin
  assert_one_char
  # states 0 and 1
  run env STUB_RC=2 PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  assert_one_char
  run env PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$TEST_DIR/does-not-exist" "${PLUGIN[@]}"
  assert_one_char
  # malformed output degrades to a one-character title as well
  printf 'not json' >"$TEST_DIR/status.json"
  plugin
  assert_one_char
}

@test "the title carries no leftover words or digits" {
  used_at 63
  [[ $(title) != *"5h"* && $(title) != *"%"* && $(title) != *"left"* ]]
}

# --- state 2: limit hit -----------------------------------------------------

@test "limit hit on 5h: red no-entry title, details in the first dropdown row" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [ "$(title)" = "⛔ | color=#cc3333" ]
  [ "$(row1)" = "⛔ 5h LIMIT · resets 23:10 (24m) | color=#cc3333" ]
  [[ $output == *"5h window limit reached"* ]]
  [[ $output == *"██████████████████ 100%"* ]]
}

@test "limit hit window row keeps only the reset clock" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [[ $output == *$'\nresets 23:10\n'* ]]
  [[ $output != *"24m left"* ]]
}

@test "boundary: 99.9 is not a limit, exactly 100 is" {
  status ".rate_limits.five_hour = {used_pct: 99.9, resets_at: \"$(at 1440)\"}"
  plugin
  [ "$(glyph)" = "●" ]
  [[ $(row1) == "5h 99%"* ]]
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [ "$(glyph)" = "⛔" ]
  [[ $(row1) == "⛔ 5h LIMIT"* ]]
}

@test "limit hit on 7d shows the weekday clock and Dd Hh countdown in the first row" {
  # reset in 3d 4h = 273600s; local clock rolls to Sun 02:46 -> use that exact text.
  status ".rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 273600)\"}"
  plugin
  [ "$(title)" = "⛔ | color=#cc3333" ]
  [ "$(row1)" = "⛔ 7d LIMIT · resets Sun 02:46 (3d 4h) | color=#cc3333" ]
}

@test "both windows limited: the later reset wins" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"} | .rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 273600)\"}"
  plugin
  [[ $(row1) == "⛔ 7d LIMIT"* ]]
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 3000)\"} | .rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 1200)\"}"
  plugin
  [[ $(row1) == "⛔ 5h LIMIT"* ]]
}

@test "7d limited with an expired 5h reading is still a limit hit" {
  status ".rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at -30)\"} | .rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 273600)\"}"
  plugin
  [ "$(glyph)" = "⛔" ]
  [[ $(row1) == "⛔ 7d LIMIT"* ]]
}

@test "limit hit stays red even when the reading is stale" {
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [ "$(title)" = "⛔ | color=#cc3333" ]
  [[ $output == *"reading 10 min old"* ]]
}

@test "a limit with an unknown or past reset is not state 2" {
  status ".rate_limits.seven_day = {used_pct: 100} | .rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(glyph)" = "○" ]
  [[ $(row1) == "5h 10%"* ]]
  status ".rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at -5)\"} | .rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  plugin
  [[ $(row1) == "5h 10%"* ]]
}

@test "blocker=usage_limit alone does not trigger the limit state" {
  status ".rate_limits.five_hour = {used_pct: 40, resets_at: \"$(at 6720)\"} | .sessions = [{status: \"blocked\", blocker: \"usage_limit\"}]"
  plugin
  [ "$(glyph)" = "◑" ]
  [[ $(row1) == "5h 40%"* ]]
}

@test "limit state counts sessions blocked on usage_limit" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"} | .sessions = [{status: \"blocked\", blocker: \"usage_limit\"}, {status: \"blocked\", blocker: \"usage_limit\"}, {status: \"blocked\", blocker: \"other\"}]"
  plugin
  [[ $output == *"2 sessions blocked on usage limit"* ]]
}

# --- state 3: expired reading ----------------------------------------------

@test "expired reading: resets_at in the past renders a gray dash and a first-row note" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at -1)\"}"
  plugin
  [ "$(title)" = "– | color=#888888" ]
  [ "$(row1)" = "5h reading expired | color=#888888" ]
  [[ $output == *"reading expired, waiting for next status-line capture"* ]]
}

@test "expired reading: resets_at equal to now is expired" {
  status ".rate_limits.five_hour = {used_pct: 30, resets_at: \"$(at 0)\"}"
  plugin
  [ "$(glyph)" = "–" ]
}

@test "expired reading shows no usage bar or window row" {
  status ".rate_limits.five_hour = {used_pct: 30, resets_at: \"$(at -10)\"}"
  plugin
  [[ $output != *"█"* && $output != *"░"* ]]
  [[ $output != *"left"* ]]
}

# --- state 5: no data -------------------------------------------------------

@test "no five_hour window is no-data, with no cost fallback" {
  status ".active_block = {id: \"b\", cost_usd: 396}"
  plugin
  [ "$(title)" = "? | color=#888888" ]
  [ "$(row1)" = "5h usage unknown | color=#888888" ]
  [[ $output != *"396"* ]]
}

@test "missing rate_limits entirely is no-data" {
  status "del(.rate_limits)"
  plugin
  [ "$(title)" = "? | color=#888888" ]
  [ "$(row1)" = "5h usage unknown | color=#888888" ]
}

@test "a window with resets_at but no used_pct is no-data" {
  status ".rate_limits.five_hour = {resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "? | color=#888888" ]
}

@test "malformed JSON degrades to no-data, not a raw error" {
  printf '{"sessions": [' >"$TEST_DIR/status.json"
  plugin
  [ "$status" -eq 0 ]
  [ "$(title)" = "? | color=#888888" ]
  [ "$(row1)" = "no 5h usage reading yet | color=#888888" ]
}

@test "non-object JSON degrades to no-data" {
  printf '[1,2,3]' >"$TEST_DIR/status.json"
  plugin
  [ "$(title)" = "? | color=#888888" ]
}

# --- states 0 and 1 ---------------------------------------------------------

@test "daemon unreachable: stub exit 2 shows a gray warning glyph, the message row, no toggles" {
  status ".rate_limits.five_hour = {used_pct: 10, resets_at: \"$(at 6720)\"}"
  run env STUB_RC=2 PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "$(title)" = "⚠ | color=#888888" ]
  [ "$(row1)" = "pa-monitor daemon unreachable | color=#888888" ]
  [[ $output == *"Refresh | refresh=true"* ]]
  [[ $output != *"Caffeinate"* && $output != *"Auto-resume"* ]]
}

@test "binary missing: PA_MONITOR_BIN not executable shows not found" {
  run env PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$TEST_DIR/does-not-exist" "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "$(title)" = "⚠ | color=#888888" ]
  [ "$(row1)" = "pa-monitor not found | color=#888888" ]
  [[ $output != *"Caffeinate"* ]]
}

@test "stub exit 127 shows not found" {
  run env STUB_RC=127 PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  [[ $output == *"pa-monitor not found"* ]]
}

@test "without PA_MONITOR_BIN the binary is looked up on PATH" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}"
  mkdir -p "$TEST_DIR/bin"
  cp "$STUB" "$TEST_DIR/bin/pa-monitor"
  run env -u PA_MONITOR_BIN PATH="$TEST_DIR/bin:$PATH" PA_SWIFTBAR_NOW="$NOW" "${PLUGIN[@]}"
  [[ $(row1) == "5h 50%"* ]]
  [[ $output == *"bash=$TEST_DIR/bin/pa-monitor "* ]]
}

@test "nothing resolvable shows not found" {
  # A PATH holding only what the renderer needs (bash for the shebang, jq,
  # timeout) and no pa-monitor.
  mkdir -p "$TEST_DIR/minimal"
  local tool
  for tool in bash jq timeout; do
    ln -s "$(command -v "$tool")" "$TEST_DIR/minimal/$tool"
  done
  run env -u PA_MONITOR_BIN PATH="$TEST_DIR/minimal" PA_SWIFTBAR_NOW="$NOW" "${PLUGIN[@]}"
  [[ $output == *"pa-monitor not found"* ]]
}

# --- clock and countdown formats -------------------------------------------

@test "countdown formats: Mm, Hh Mm, Dd Hh" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 59)\"}"
  plugin
  [[ $(row1) == *"(0m)"* ]]
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 3599)\"}"
  plugin
  [[ $(row1) == *"(59m)"* ]]
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 3600)\"}"
  plugin
  [[ $(row1) == *"(1h 0m)"* ]]
  status ".rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 86399)\"}"
  plugin
  [[ $(row1) == *"(23h 59m)"* ]]
  status ".rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 86400)\"}"
  plugin
  [[ $(row1) == *"(1d 0h)"* ]]
}

@test "reset clock is HH:MM today and Ddd HH:MM on another date" {
  # NOW is 22:46 local; +24m = 23:10 same date; +74m = 00:00 next date (Thu).
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [[ $(row1) == *"resets 23:10 "* ]]
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 4440)\"}"
  plugin
  [[ $(row1) == *"resets Thu 00:00 "* ]]
}

@test "clock honours the process timezone" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  run env TZ=UTC PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  [[ $(row1) == *"resets 04:10 "* ]]
}

# --- dropdown content per state --------------------------------------------

@test "usage bar and a reset-clock-only window row appear in state 4" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}"
  plugin
  [[ $output == *"█████████░░░░░░░░░ 50%"* ]]
  # The countdown lives in the first row; the window row keeps only the clock.
  [[ $output == *$'\nresets Thu 00:38\n'* ]]
  [[ $output != *"resets Thu 00:38 · "* ]]
  [ "$(printf '%s\n' "$output" | grep -c '1h 52m')" -eq 1 ]
}

@test "the first dropdown row is the line right after the first separator" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(printf '%s\n' "$output" | sed -n '2p')" = "---" ]
}

@test "bar and window row are absent in states 3 and 5, sessions and toggles remain" {
  status ".rate_limits.five_hour = {used_pct: 30, resets_at: \"$(at -10)\"}"
  plugin
  [[ $output != *"█"* && $output != *"░"* ]]
  [[ $output == *"working"* && $output == *"Caffeinate:"* && $output == *"Refresh"* ]]
  status "del(.rate_limits)"
  plugin
  [[ $output != *"█"* && $output != *"░"* ]]
  [[ $output == *"working"* && $output == *"Caffeinate:"* && $output == *"Refresh"* ]]
}

@test "seven-day line appears when known" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .rate_limits.seven_day = {used_pct: 42.5, resets_at: \"$(at 273600)\"}"
  plugin
  [[ $output == *"7d: 42% · resets Sun 02:46"* ]]
}

@test "seven-day line without a reset omits the reset text" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .rate_limits.seven_day = {used_pct: 42}"
  plugin
  [[ $output == *"7d: 42%"* ]]
  [[ $output != *"7d: 42% ·"* ]]
}

@test "sessions row counts by status" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .sessions = [{status: \"working\"}, {status: \"working\"}, {status: \"blocked\"}, {status: \"idle\"}, {status: \"idle\"}, {status: \"idle\"}]"
  plugin
  [[ $output == *"2 working · 1 blocked · 3 idle"* ]]
}

# --- toggles ----------------------------------------------------------------

@test "toggle rows are absent when the new keys are absent (older client)" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | del(.caffeinate, .auto_resume)"
  plugin
  [[ $output != *"Caffeinate"* && $output != *"Auto-resume"* ]]
  [[ $output == *"Refresh | refresh=true"* ]]
}

# caffeinate MODE PROCESS -> sets the fixture's caffeinate object.
caf() {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .caffeinate = {mode: $1, process: \"$2\"}${3:+ | .caffeinate.grace_remaining_s = $3}"
  plugin
}

caffeinate_row() { printf '%s\n' "$output" | grep '^Caffeinate:'; }

@test "caffeinate matrix: mode on" {
  caf true holding
  [ "$(caffeinate_row)" = "Caffeinate: on (holding) | checked=true bash=$STUB param1=caffeinate param2=off terminal=false refresh=true" ]
  caf true off
  [[ $(caffeinate_row) == "Caffeinate: on (armed) | checked=true "* ]]
  caf true error
  [[ $(caffeinate_row) == "Caffeinate: on (error) | checked=true "* ]]
  caf true grace 42
  [[ $(caffeinate_row) == "Caffeinate: on (grace 0:42) | checked=true "* ]]
}

@test "caffeinate matrix: mode off" {
  caf false off
  [ "$(caffeinate_row)" = "Caffeinate: off | bash=$STUB param1=caffeinate param2=on terminal=false refresh=true" ]
  caf false holding
  [[ $(caffeinate_row) == "Caffeinate: off (still holding) | bash="* ]]
  caf false error
  [[ $(caffeinate_row) == "Caffeinate: off (error) | bash="* ]]
  caf false grace 125
  [[ $(caffeinate_row) == "Caffeinate: off (releasing 2:05) | bash="* ]]
}

@test "caffeinate grace without grace_remaining_s renders 0:00" {
  caf true grace
  [[ $(caffeinate_row) == "Caffeinate: on (grace 0:00) |"* ]]
  caf false grace
  [[ $(caffeinate_row) == "Caffeinate: off (releasing 0:00) |"* ]]
}

@test "caffeinate unknown or unrecognised process renders (?) for both modes" {
  caf true unknown
  [[ $(caffeinate_row) == "Caffeinate: on (?) | checked=true "* ]]
  caf false "from-a-newer-daemon"
  [[ $(caffeinate_row) == "Caffeinate: off (?) | bash="* ]]
}

@test "caffeinate click is relative to the rendered state, never toggle" {
  caf true holding
  [[ $output == *"param2=off"* && $output != *"param2=toggle"* ]]
  caf false off
  [[ $output == *"param1=caffeinate param2=on"* && $output != *"param2=toggle"* ]]
}

@test "checked follows mode only, not process" {
  caf true error
  [[ $(caffeinate_row) == *"checked=true"* ]]
  caf false holding
  [[ $(caffeinate_row) != *"checked=true"* ]]
}

@test "auto-resume row: on is checked and clicks off, off clicks on" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .auto_resume = true"
  plugin
  [[ $output == *"Auto-resume: on | checked=true bash=$STUB param1=auto-resume param2=off terminal=false refresh=true"* ]]
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .auto_resume = false"
  plugin
  [[ $output == *"Auto-resume: off | bash=$STUB param1=auto-resume param2=on terminal=false refresh=true"* ]]
}

@test "toggle rows also appear in the limit-hit state" {
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [[ $output == *"Caffeinate: off"* && $output == *"Auto-resume: off"* ]]
}

@test "Refresh row is the last line" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(printf '%s\n' "$output" | tail -n1)" = "Refresh | refresh=true" ]
}

# --- title width: wide / narrow toggle (bead pg2-fiw4h) ----------------------
# wide = the pre-pg2-ucet4 title and dropdown layout, the default; narrow = the
# one-character title with the detail in the first dropdown row. The setting is
# $XDG_STATE_HOME/pa-monitor-swiftbar/title-width (isolated per test).

# line N -> the Nth output line.
line() { printf '%s\n' "$output" | sed -n "${1}p"; }

# render_state NAME -> sets up the fixture for one named state and renders it.
render_state() {
  case "$1" in
  normal) status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}" ;;
  limit) status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}" ;;
  expired) status ".rate_limits.five_hour = {used_pct: 30, resets_at: \"$(at -10)\"}" ;;
  nodata) status "del(.rate_limits)" ;;
  esac
  case "$1" in
  unreachable) run env STUB_RC=2 PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}" ;;
  notfound) run env PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$TEST_DIR/does-not-exist" "${PLUGIN[@]}" ;;
  malformed)
    printf 'not json' >"$TEST_DIR/status.json"
    plugin
    ;;
  *) plugin ;;
  esac
}

@test "title width: no setting file reads as wide" {
  rm -f "$TITLE_WIDTH_FILE"
  render_state normal
  [ "$status" -eq 0 ]
  [ "$(title)" = "5h 50% · 1h 52m | color=#3a9a4a" ]
}

@test "title width: an empty file reads as wide" {
  : >"$TITLE_WIDTH_FILE"
  render_state normal
  [ "$(title)" = "5h 50% · 1h 52m | color=#3a9a4a" ]
}

@test "title width: an unrecognised value reads as wide" {
  local v
  for v in "huge" "Narrow" "narrow-ish" "0" "wide narrow"; do
    set_width "$v"
    render_state normal
    [ "$status" -eq 0 ]
    [ "$(title)" = "5h 50% · 1h 52m | color=#3a9a4a" ] || {
      echo "value '$v' did not read as wide" >&2
      return 1
    }
  done
}

@test "title width: surrounding whitespace around narrow is ignored" {
  printf '  narrow  \n' >"$TITLE_WIDTH_FILE"
  render_state normal
  [ "$(title)" = "◑ | color=#3a9a4a" ]
}

@test "title width: a missing state directory reads as wide, not an error" {
  rm -rf "$XDG_STATE_HOME"
  render_state normal
  [ "$status" -eq 0 ]
  [ "$(title)" = "5h 50% · 1h 52m | color=#3a9a4a" ]
}

@test "wide normal: old title, no duplicate detail row, window row with the countdown" {
  set_width wide
  status ".rate_limits.five_hour = {used_pct: 50.9, resets_at: \"$(at 6720)\"} | .rate_limits.seven_day = {used_pct: 42.5, resets_at: \"$(at 273600)\"}"
  plugin
  [ "$status" -eq 0 ]
  [ "$(line 1)" = "5h 50% · 1h 52m | color=#3a9a4a" ]
  [ "$(line 2)" = "---" ]
  [ "$(line 3)" = "█████████░░░░░░░░░ 50%" ]
  [ "$(line 4)" = "7d: 42% · resets Sun 02:46" ]
  [ "$(line 5)" = "resets Thu 00:38 · 1h 52m left" ]
  # The detail appears once, in the title; nothing in the dropdown repeats it.
  [[ $output != *$'\n5h 50%'* ]]
}

@test "wide normal: colors follow the same thresholds (yellow, red, stale variants)" {
  set_width wide
  status ".rate_limits.five_hour = {used_pct: 63.4, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "5h 63% · 1h 52m | color=#e0b000" ]
  used_at 80
  [ "$(title)" = "5h 80% · 1h 52m | color=#cc3333" ]
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 90, resets_at: \"$(at 6720)\"}"
  plugin
  [ "$(title)" = "5h 90% · 1h 52m | color=#7a2b2b" ]
  [[ $output == *"reading 10 min old"* ]]
}

@test "wide normal: unknown resets_at drops the countdown from the title and the window row" {
  set_width wide
  status ".rate_limits.five_hour = {used_pct: 63}"
  plugin
  [ "$(title)" = "5h 63% | color=#3a9a4a" ]
  [[ $output != *"left"* && $output != *"resets"* ]]
}

@test "wide normal: used 100 with an unknown reset is a red 5h 100%, not the limit state" {
  set_width wide
  status ".rate_limits.five_hour = {used_pct: 100}"
  plugin
  [ "$(title)" = "5h 100% | color=#cc3333" ]
}

@test "wide limit hit on 5h: LIMIT with the reset clock and countdown, window row with the countdown" {
  set_width wide
  render_state limit
  [ "$(line 1)" = "⛔ LIMIT · resets 23:10 (24m) | color=#cc3333" ]
  [ "$(line 2)" = "---" ]
  [ "$(line 3)" = "5h window limit reached" ]
  [ "$(line 4)" = "██████████████████ 100%" ]
  [ "$(line 5)" = "resets 23:10 · 24m left" ]
  [[ $output == *"0 sessions blocked on usage limit"* ]]
}

@test "wide limit hit on 7d: the 7d prefix, weekday clock and Dd Hh countdown" {
  set_width wide
  status ".rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 273600)\"}"
  plugin
  [ "$(title)" = "⛔ 7d LIMIT · resets Sun 02:46 (3d 4h) | color=#cc3333" ]
  [[ $output == *$'\nresets Sun 02:46 · 3d 4h left\n'* ]]
}

@test "wide limit hit: the later reset wins, stale stays red, 99.9 is not a limit" {
  set_width wide
  status ".rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"} | .rate_limits.seven_day = {used_pct: 100, resets_at: \"$(at 273600)\"}"
  plugin
  [[ $(title) == "⛔ 7d LIMIT"* ]]
  status ".rate_limits.captured_at = \"$(at -601)\" | .rate_limits.five_hour = {used_pct: 100, resets_at: \"$(at 1440)\"}"
  plugin
  [ "$(title)" = "⛔ LIMIT · resets 23:10 (24m) | color=#cc3333" ]
  status ".rate_limits.five_hour = {used_pct: 99.9, resets_at: \"$(at 1440)\"}"
  plugin
  [[ $(title) == "5h 99% · 24m | "* ]]
}

@test "wide expired reading: 5h dash title, then the explanatory row" {
  set_width wide
  render_state expired
  [ "$(line 1)" = "5h – | color=#888888" ]
  [ "$(line 2)" = "---" ]
  [ "$(line 3)" = "reading expired, waiting for next status-line capture | color=#888888" ]
  [[ $output != *"█"* && $output != *"left"* ]]
}

@test "wide no data: 5h question-mark title, then the note" {
  set_width wide
  render_state nodata
  [ "$(line 1)" = "5h ? | color=#888888" ]
  [ "$(line 2)" = "---" ]
  [ "$(line 3)" = "no 5h usage reading yet | color=#888888" ]
}

@test "wide malformed JSON degrades to the 5h question-mark title" {
  set_width wide
  render_state malformed
  [ "$status" -eq 0 ]
  [ "$(line 1)" = "5h ? | color=#888888" ]
  [ "$(line 3)" = "no 5h usage reading yet | color=#888888" ]
}

@test "wide not found and unreachable: 5h warning title and the message row" {
  set_width wide
  render_state notfound
  [ "$(line 1)" = "5h ⚠ | color=#888888" ]
  [ "$(line 3)" = "pa-monitor not found | color=#888888" ]
  render_state unreachable
  [ "$(line 1)" = "5h ⚠ | color=#888888" ]
  [ "$(line 3)" = "pa-monitor daemon unreachable | color=#888888" ]
  [[ $output != *"Caffeinate"* ]]
  run env STUB_RC=127 PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  [ "$(line 1)" = "5h ⚠ | color=#888888" ]
}

@test "wide keeps the sessions row, data-age row, and caffeinate / auto-resume rows unchanged" {
  set_width wide
  status ".rate_limits.captured_at = \"$(at -840)\" | .rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"} | .sessions = [{status: \"working\"}, {status: \"blocked\"}, {status: \"idle\"}] | .caffeinate = {mode: true, process: \"holding\"} | .auto_resume = true"
  plugin
  [[ $output == *$'\n1 working · 1 blocked · 1 idle\n'* ]]
  [[ $output == *$'\nreading 14 min old | color=#888888\n'* ]]
  [[ $output == *"Caffeinate: on (holding) | checked=true bash=$STUB param1=caffeinate param2=off terminal=false refresh=true"* ]]
  [[ $output == *"Auto-resume: on | checked=true bash=$STUB param1=auto-resume param2=off terminal=false refresh=true"* ]]
}

@test "narrow: the Title row is the only addition to the pre-toggle dropdown, just above Refresh" {
  set_width narrow
  render_state normal
  [ "$(title)" = "◑ | color=#3a9a4a" ]
  [ "$(line 3)" = "5h 50% · 1h 52m left | color=#3a9a4a" ]
  [ "$(line 5)" = "resets Thu 00:38" ]
  local n
  n=$(printf '%s\n' "$output" | wc -l | tr -d ' ')
  [[ "$(line $((n - 1)))" == "Title: narrow (click for wide) | bash="* ]]
  [ "$(line "$n")" = "Refresh | refresh=true" ]
}

@test "title width: the toggle row shows the mode and the action, in every state, both widths" {
  local mode state other
  for mode in wide narrow; do
    set_width "$mode"
    if [ "$mode" = wide ]; then other=narrow; else other=wide; fi
    for state in normal limit expired nodata malformed notfound unreachable; do
      render_state "$state"
      [ "$status" -eq 0 ]
      [[ $output == *$'\nTitle: '"$mode"' (click for '"$other"') | bash='*" param1=--set-title-width param2=$other terminal=false refresh=true"$'\n'* ]] || {
        echo "no toggle row for $mode/$state" >&2
        return 1
      }
      # Refresh stays the last line.
      [ "$(printf '%s\n' "$output" | tail -n1)" = "Refresh | refresh=true" ]
    done
  done
}

@test "title width: no setting file shows the wide mode in the toggle row" {
  rm -f "$TITLE_WIDTH_FILE"
  render_state normal
  [[ $output == *$'\nTitle: wide (click for narrow) | bash='* ]]
}

@test "title width: the toggle row runs this script by its own path" {
  render_state normal
  local row path
  row="$(grep '^Title: ' <<<"$output")"
  path="${row#*bash=}"
  path="${path%% param1=*}"
  [ "$path" = "${PLUGIN[${#PLUGIN[@]} - 1]}" ]
}

@test "title width: PA_SWIFTBAR_SELF (set by the nix wrapper) is what the toggle row runs" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}"
  run env PA_SWIFTBAR_SELF=/nix/store/abc-pa-monitor-swiftbar/bin/pa-monitor-swiftbar PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  [[ $output == *"| bash=/nix/store/abc-pa-monitor-swiftbar/bin/pa-monitor-swiftbar param1=--set-title-width "* ]]
}

@test "title width: a script path with whitespace is quoted in the toggle row" {
  status ".rate_limits.five_hour = {used_pct: 50, resets_at: \"$(at 6720)\"}"
  run env PA_SWIFTBAR_SELF="/tmp/with space/pa-monitor-swiftbar" PA_SWIFTBAR_NOW="$NOW" PA_MONITOR_BIN="$STUB" "${PLUGIN[@]}"
  [[ $output == *'| bash="/tmp/with space/pa-monitor-swiftbar" param1=--set-title-width '* ]]
}

@test "--set-title-width writes the file, prints nothing, and never calls pa-monitor" {
  rm -rf "$XDG_STATE_HOME"
  run env PA_MONITOR_BIN="$TEST_DIR/does-not-exist" "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$(cat "$TITLE_WIDTH_FILE")" = "narrow" ]
  # Exactly `narrow` plus one newline.
  [ "$(wc -c <"$TITLE_WIDTH_FILE" | tr -d ' ')" -eq 7 ]
  run "${PLUGIN[@]}" --set-title-width wide
  [ "$status" -eq 0 ]
  [ "$(cat "$TITLE_WIDTH_FILE")" = "wide" ]
  [ "$(wc -c <"$TITLE_WIDTH_FILE" | tr -d ' ')" -eq 5 ]
}

@test "--set-title-width is atomic: no temp file is left beside the setting" {
  rm -rf "$XDG_STATE_HOME"
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  [ "$(ls -A "$XDG_STATE_HOME/pa-monitor-swiftbar")" = "title-width" ]
  run "${PLUGIN[@]}" --set-title-width wide
  [ "$(ls -A "$XDG_STATE_HOME/pa-monitor-swiftbar")" = "title-width" ]
}

@test "--set-title-width rejects an unknown mode and a missing value with exit 2, writing nothing" {
  rm -rf "$XDG_STATE_HOME"
  run "${PLUGIN[@]}" --set-title-width huge
  [ "$status" -eq 2 ]
  run "${PLUGIN[@]}" --set-title-width
  [ "$status" -eq 2 ]
  run "${PLUGIN[@]}" --set-title-width Wide
  [ "$status" -eq 2 ]
  [ ! -e "$TITLE_WIDTH_FILE" ]
  [ ! -e "$XDG_STATE_HOME/pa-monitor-swiftbar" ]
}

@test "--set-title-width with a bad value keeps the existing setting" {
  set_width narrow
  run "${PLUGIN[@]}" --set-title-width huge
  [ "$status" -eq 2 ]
  [ "$(cat "$TITLE_WIDTH_FILE")" = "narrow" ]
}

@test "--set-title-width with XDG_STATE_HOME unset falls back to HOME/.local/state" {
  run env -u XDG_STATE_HOME HOME="$TEST_DIR/home2" "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  [ "$(cat "$TEST_DIR/home2/.local/state/pa-monitor-swiftbar/title-width")" = "narrow" ]
}

@test "--set-title-width reports an unwritable state directory and exits non-zero" {
  : >"$TEST_DIR/blocker"
  run env XDG_STATE_HOME="$TEST_DIR/blocker" "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -ne 0 ]
}

@test "title width: clicking the toggle row's action flips the next render, and back" {
  rm -f "$TITLE_WIDTH_FILE"
  render_state normal
  [ "$(title)" = "5h 50% · 1h 52m | color=#3a9a4a" ]
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  plugin
  [ "$(title)" = "◑ | color=#3a9a4a" ]
  [ "$(line 3)" = "5h 50% · 1h 52m left | color=#3a9a4a" ]
  run "${PLUGIN[@]}" --set-title-width wide
  plugin
  [ "$(title)" = "5h 50% · 1h 52m | color=#3a9a4a" ]
}

@test "title width: the setter works with only the isolated tool set on PATH" {
  mkdir -p "$TEST_DIR/minimal"
  local tool
  for tool in bash mkdir mktemp mv rm dirname; do
    ln -s "$(command -v "$tool")" "$TEST_DIR/minimal/$tool"
  done
  rm -rf "$XDG_STATE_HOME"
  run env PATH="$TEST_DIR/minimal" "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  [ "$(cat "$TITLE_WIDTH_FILE")" = "narrow" ]
}

@test "--help documents the title width setting and --set-title-width" {
  run "${PLUGIN[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"--set-title-width"* ]]
  [[ $output == *"title-width"* ]]
  [[ $output == *"wide"* && $output == *"narrow"* ]]
  [[ $output == *"PA_SWIFTBAR_SELF"* ]]
}
