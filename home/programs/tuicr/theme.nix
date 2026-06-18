# Maps the active Stylix base16 palette onto tuicr's theme tokens.
#
# Called with: { colors = config.lib.stylix.colors; lib = <nixpkgs lib>; }
# where colors.baseXX is a 6-char lowercase hex string (no leading #).
# Returns a flat attrset of tuicr token -> "#RRGGBB", rendered to TOML by the
# module (and validated by the test-tuicr-theme flake check).
#
# Schema + value format confirmed against tuicr v0.17.1's own reference theme:
#   github.com/agavra/tuicr/blob/v0.17.1/examples/tuicr-teal.toml
# (flat top-level keys, "#RRGGBB" hex). Every documented token is required;
# `syntax_theme` is the only optional one and is omitted so tuicr auto-selects a
# bundled syntax theme matching this (dark) theme's background brightness.
#
# Slot choices follow the base16 semantic role spec:
#   github.com/chriskempson/base16/blob/main/styling.md
# Tuned for a dark scheme (the configured polarity); on a light scheme the
# inverse `message_*_fg = base00` choices would want revisiting.
{ colors, lib }:
let
  c = colors;

  # Hex <-> RGB helpers, used only to blend tinted diff backgrounds. base16
  # defines 16 fixed semantic roles with NO tinted-background variants, so a
  # readable green/red diff background must be computed by mixing the accent
  # toward the editor background. (claude-theme/colors.nix flagged this same
  # limitation as future work; tuicr is a diff tool, so we do the blend here.)
  hexDigits = {
    "0" = 0;
    "1" = 1;
    "2" = 2;
    "3" = 3;
    "4" = 4;
    "5" = 5;
    "6" = 6;
    "7" = 7;
    "8" = 8;
    "9" = 9;
    "a" = 10;
    "b" = 11;
    "c" = 12;
    "d" = 13;
    "e" = 14;
    "f" = 15;
  };
  hexToDec =
    s: lib.foldl (acc: ch: acc * 16 + hexDigits.${ch}) 0 (lib.stringToCharacters (lib.toLower s));
  hexToRGB = hex: {
    r = hexToDec (lib.substring 0 2 hex);
    g = hexToDec (lib.substring 2 2 hex);
    b = hexToDec (lib.substring 4 2 hex);
  };
  toHex2 = n: lib.fixedWidthString 2 "0" (lib.toLower (lib.toHexString n));

  # Mix slots `aHex` and `bHex`, weighting `aHex` by `pct`% (integer math; Nix
  # int/int division truncates). Returns a 6-char lowercase hex (no leading #).
  mix =
    aHex: bHex: pct:
    let
      a = hexToRGB aHex;
      b = hexToRGB bHex;
      ch = x: y: (x * pct + y * (100 - pct)) / 100;
    in
    "${toHex2 (ch a.r b.r)}${toHex2 (ch a.g b.g)}${toHex2 (ch a.b b.b)}";
in
{
  # Surfaces (base00 darkest -> base02 brightest highlight)
  panel_bg = "#${c.base00}"; # Default Background
  status_bar_bg = "#${c.base01}"; # Lighter Background (status bars)
  cursor_line_bg = "#${c.base01}"; # current-line tint
  bg_highlight = "#${c.base02}"; # Selection Background

  # Foregrounds
  fg_primary = "#${c.base05}"; # Default Foreground
  fg_secondary = "#${c.base04}"; # Dark Foreground
  fg_dim = "#${c.base03}"; # Comments / Invisibles
  diff_context = "#${c.base05}"; # unchanged lines = normal text
  expanded_context_fg = "#${c.base03}"; # expanded context = dimmed
  help_indicator = "#${c.base04}";
  branch_name = "#${c.base0E}"; # git branch (accent)

  # Diff foregrounds + computed tinted backgrounds
  diff_add = "#${c.base0B}"; # green - inserted
  diff_del = "#${c.base08}"; # red - deleted
  diff_hunk_header = "#${c.base0D}"; # blue - heading
  diff_add_bg = "#${mix c.base0B c.base00 18}"; # subtle green tint
  diff_del_bg = "#${mix c.base08 c.base00 18}"; # subtle red tint
  syntax_add_bg = "#${mix c.base0B c.base00 30}"; # stronger (intra-line)
  syntax_del_bg = "#${mix c.base08 c.base00 30}";

  # File status
  file_added = "#${c.base0B}"; # green
  file_modified = "#${c.base0A}"; # yellow
  file_deleted = "#${c.base08}"; # red
  file_renamed = "#${c.base0E}"; # purple

  # Review status
  reviewed = "#${c.base0B}"; # green - done
  pending = "#${c.base0A}"; # yellow - outstanding

  # Comment kinds
  comment_note = "#${c.base0D}"; # blue
  comment_suggestion = "#${c.base0C}"; # cyan
  comment_issue = "#${c.base08}"; # red
  comment_praise = "#${c.base0B}"; # green

  # Borders / cursor
  border_focused = "#${c.base0E}"; # accent
  border_unfocused = "#${c.base02}"; # muted
  cursor_color = "#${c.base0A}"; # high-visibility caret

  # Inline message + badge pills: fg = background so text reads inverse on the
  # accent-colored pill (same rationale as claude-theme's inverseText = base00).
  message_info_fg = "#${c.base00}";
  message_info_bg = "#${c.base0D}"; # blue
  message_warning_fg = "#${c.base00}";
  message_warning_bg = "#${c.base0A}"; # yellow
  message_error_fg = "#${c.base00}";
  message_error_bg = "#${c.base08}"; # red
  update_badge_fg = "#${c.base00}";
  update_badge_bg = "#${c.base0A}"; # yellow
  mode_fg = "#${c.base00}";
  mode_bg = "#${c.base0C}"; # cyan
}
