# Stylix → Claude Code theme integration.
#
# When enabled, this module:
#   1. Generates ~/.claude/themes/stylix.json with a full base16 token mapping.
#   2. Sets the theme preference in ~/.claude/settings.json to "custom:stylix".
#   3. Injects Stylix 24-bit truecolor escapes into the status-line segment scripts.
#
# All color values are resolved at nix eval time, consistent with how Stylix
# manages themes for other applications in this configuration.
#
# home.file creates a symlink into the nix store, so the theme file is read-only.
# Claude Code's interactive theme editor (Ctrl+E in /theme) cannot modify it.
# This is intentional — the file is declaratively managed.
#
# Requires: stylix.enable = true (enforced via assertion).
{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude.theme;
  c = config.lib.stylix.colors;

  # ---------------------------------------------------------------------------
  # Hex → truecolor escape converter (pure nix, no runtime dependency).
  #
  # Converts a 6-char lowercase hex string (e.g. "cba6f7") to a bash ANSI
  # foreground escape "\\033[38;2;R;G;Bm". The double-backslash produces a
  # literal backslash in the generated shell script; printf then expands
  # \033 as the ESC octal escape when it appears in the format string position.
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
    s:
    let
      chars = lib.stringToCharacters (lib.toLower s);
    in
    lib.foldl (acc: ch: acc * 16 + hexDigits.${ch}) 0 chars;
  hexToRGB = hex: {
    r = hexToDec (lib.substring 0 2 hex);
    g = hexToDec (lib.substring 2 2 hex);
    b = hexToDec (lib.substring 4 2 hex);
  };
  truecolorFg =
    hex:
    let
      rgb = hexToRGB (lib.toLower hex);
    in
    "\\033[38;2;${toString rgb.r};${toString rgb.g};${toString rgb.b}m";

  # ---------------------------------------------------------------------------
  # base field: Claude Code requires "dark" or "light" as the fallback preset.
  # stylix.polarity drives this. "either" (auto-detect) has no static mapping,
  # so we fall back to "dark" as the conservative default.
  polarity = config.stylix.polarity or "either";
  base = if polarity == "light" then "light" else "dark";

  tokenMap = import ./colors.nix { colors = c; };
in
{
  options.phillipgreenii.programs.claude.theme = {
    enable = lib.mkEnableOption "Stylix-aligned Claude Code theme";
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    assertions = [
      {
        assertion = config.stylix.enable;
        message = ''
          phillipgreenii.programs.claude.theme.enable = true requires stylix.enable = true.
          Enable Stylix before enabling the Claude Code Stylix theme integration.
        '';
      }
    ];

    # Generate ~/.claude/themes/stylix.json.
    # Claude Code hot-reloads files from ~/.claude/themes/, so changes apply
    # without restarting. The file is a symlink into the nix store (read-only).
    home.file.".claude/themes/stylix.json".text = builtins.toJSON {
      name = "Stylix";
      inherit base;
      overrides = tokenMap;
    };

    # Set theme preference in ~/.claude/settings.json.
    # "custom:stylix" selects ~/.claude/themes/stylix.json.
    phillipgreenii.programs.claude.settings.theme = "custom:stylix";

    # Inject Stylix 24-bit truecolor escapes into the status-line segment scripts.
    # Keys match the named colors used in scripts.nix (yellow, green, red, cyan, magenta).
    # BOLD and DIM are ANSI attribute codes (not colors) and remain hardcoded.
    phillipgreenii.programs.claude.status-line-colors = {
      yellow = truecolorFg c.base0A; # worktree name, context-warning mid
      green = truecolorFg c.base0B; # host indicator H, git branch, context OK
      red = truecolorFg c.base08; # context-warning high threshold
      cyan = truecolorFg c.base0C; # model name display
      magenta = truecolorFg c.base0E; # contained-claude C indicator (accent)
    };
  };
}
