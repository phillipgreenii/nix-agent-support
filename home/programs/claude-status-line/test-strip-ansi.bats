#!/usr/bin/env bats

# Unit tests for the visible-width strip_ansi() helper. The status-line wrapper injects this
# function verbatim via `builtins.readFile ./strip-ansi.bash` (see scripts.nix); testing the
# function DIRECTLY (sourced) is the only way to exercise BOTH OSC 8 terminator forms — the
# built wrapper only ever emits the ST form, so the BEL path has no end-to-end producer.
#
# bats runs against the whole directory (flake.nix), so this file is auto-discovered by both
# the nerd-on and nerd-off status-line check derivations. strip_ansi is nerd-font-agnostic.

setup() {
  # $BATS_TEST_DIRNAME (not a bare relative path): under `bats <dir>` the CWD is not the test dir.
  source "$BATS_TEST_DIRNAME/strip-ansi.bash"
}

ESC=$'\033'
BEL=$'\007'
ST=$'\033\\' # OSC String Terminator: ESC backslash

# --- plain text and CSI / SGR (the pre-existing behavior, now extracted) ---

@test "plain text passes through unchanged" {
  run strip_ansi "hello world"
  [ "$status" -eq 0 ]
  [ "$output" = "hello world" ]
}

@test "strips a single SGR color sequence" {
  run strip_ansi "${ESC}[32mPR#1234${ESC}[0m"
  [ "$status" -eq 0 ]
  [ "$output" = "PR#1234" ]
}

@test "strips multiple/nested SGR sequences leaving only visible text" {
  run strip_ansi "${ESC}[1m${ESC}[33mwt${ESC}[0m ${ESC}[36mbr${ESC}[0m"
  [ "$status" -eq 0 ]
  [ "$output" = "wt br" ]
}

@test "strips a non-SGR CSI (final byte != 'm') without eating visible text" {
  # ESC[32X is a well-formed CSI whose final byte is 'X' (ECH), not 'm'. Keying only on 'm'
  # would have eaten through the 'm' in the following visible text; the CSI-final-byte guard stops at 'X'.
  run strip_ansi "A${ESC}[32X mango B"
  [ "$status" -eq 0 ]
  [ "$output" = "A mango B" ]
}

@test "malformed CSI (no final byte) drops the unterminated remainder" {
  # Mirrors the malformed-OSC case: a CSI whose parameter bytes run to end-of-string with no
  # final byte drops the remainder rather than leaking the parameter bytes as visible text.
  run strip_ansi "keep${ESC}[999"
  [ "$status" -eq 0 ]
  [ "$output" = "keep" ]
}

# --- OSC 8 hyperlinks (the new behavior this bead adds) ---

@test "strips an ST-terminated OSC 8 hyperlink to just its visible text" {
  # This is exactly the shape prPart emits: OSC-open, colored PR#, OSC-close (all ST-terminated).
  local url="https://github.com/anthropics/claude-code/pull/1234"
  run strip_ansi "${ESC}]8;;${url}${ST}${ESC}[32mPR#1234${ESC}[0m${ESC}]8;;${ST}"
  [ "$status" -eq 0 ]
  [ "$output" = "PR#1234" ]
}

@test "strips a BEL-terminated OSC 8 hyperlink to just its visible text" {
  # BEL (\007) is the older xterm terminator; strip_ansi handles it defensively for custom parts.
  local url="https://github.com/anthropics/claude-code/pull/1234"
  run strip_ansi "${ESC}]8;;${url}${BEL}${ESC}[32mPR#1234${ESC}[0m${ESC}]8;;${BEL}"
  [ "$status" -eq 0 ]
  [ "$output" = "PR#1234" ]
}

@test "strips back-to-back OSC 8 hyperlinks, preserving separators" {
  local h1="${ESC}]8;;http://a${ST}one${ESC}]8;;${ST}"
  local h2="${ESC}]8;;http://b${ST}two${ESC}]8;;${ST}"
  run strip_ansi "${h1} | ${h2}"
  [ "$status" -eq 0 ]
  [ "$output" = "one | two" ]
}

@test "malformed OSC 8 (no terminator) drops the unterminated remainder" {
  run strip_ansi "visible${ESC}]8;;http://never-terminated"
  [ "$status" -eq 0 ]
  [ "$output" = "visible" ]
}

@test "visible width is independent of the hyperlink URL length" {
  # The whole point of stripping OSC 8: PR#7 measures 4 columns regardless of URL length,
  # so a long PR URL never forces a spurious wrap. len("PR#7") = 4.
  local short long
  short=$(strip_ansi "${ESC}]8;;http://x${ST}PR#7${ESC}]8;;${ST}")
  long=$(strip_ansi "${ESC}]8;;https://github.com/some/really/long/org/repo/pull/7${ST}PR#7${ESC}]8;;${ST}")
  [ "$short" = "PR#7" ]
  [ "$long" = "PR#7" ]
  [ "${#short}" -eq "${#long}" ]
  [ "${#short}" -eq 4 ]
}
