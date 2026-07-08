# shellcheck shell=bash
# Strip ANSI escape sequences to compute a segment's VISIBLE width.
#
# Handles the two escape families the status line emits:
#   * CSI / SGR color:  ESC '[' ... final              (e.g. \033[32m ... \033[0m). The status line
#     only ever emits SGR (final byte 'm'), but any CSI final byte (0x40-0x7E) terminates the strip
#     so a hand-crafted non-SGR / malformed CSI in a custom part cannot eat visible text.
#   * OSC 8 hyperlink:  ESC ']' 8 ; params ; URI <term> (e.g. \033]8;;URL\033\ text \033]8;;\033\)
#     where <term> is EITHER ST (ESC '\') OR BEL (\007). Both terminator forms are stripped so a
#     clickable-but-zero-visible-width hyperlink (its URI can be long) never inflates the width.
#
# Pure bash (no subprocess). Any other lone ESC is dropped (it has zero visible width). Each
# loop iteration consumes at least the leading ESC, so the scan always terminates.
strip_ansi() {
  local s=$1 out="" rest pre_bel pre_st
  local LC_COLLATE=C # byte-exact ranges ([@-~]) regardless of the active collation locale
  while [ "$s" != "${s#*$'\033'}" ]; do
    out=$out${s%%$'\033'*} # visible text before the ESC
    rest=${s#*$'\033'}     # everything after the first ESC
    case $rest in
    '['*) # CSI / SGR: drop '[' through the CSI final byte (0x40-0x7E, i.e. [@-~]); 'm' is the SGR case
      rest=${rest#\[}
      if [ "$rest" != "${rest#*[@-~]}" ]; then
        rest=${rest#*[@-~]} # final byte present: drop the parameter/intermediate bytes and the final byte
      else
        rest="" # malformed CSI (no final byte): drop the remainder
      fi
      ;;
    ']'*) # OSC: drop ']' through the ST or BEL terminator
      rest=${rest#\]}
      pre_bel=${rest%%$'\007'*} # text before first BEL  (== rest when no BEL present)
      pre_st=${rest%%$'\033'*}  # text before first ESC  (== rest when no ESC present)
      if [ "$pre_bel" != "$rest" ] && { [ "$pre_st" = "$rest" ] || [ ${#pre_bel} -lt ${#pre_st} ]; }; then
        rest=${rest#*$'\007'} # BEL terminates first: drop through the BEL
      elif [ "$pre_st" != "$rest" ]; then
        rest=${rest#*$'\033'} # ST terminates first: drop through the ESC ...
        rest=${rest#\\}       # ... and its trailing backslash
      else
        rest="" # malformed OSC (no terminator): drop the remainder
      fi
      ;;
    *) ;; # other/lone ESC: already dropped; keep scanning
    esac
    s=$rest
  done
  printf '%s' "$out$s"
}
