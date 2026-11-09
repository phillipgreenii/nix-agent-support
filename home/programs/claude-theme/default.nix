# Stylix → Claude Code theme integration.
#
# When enabled, this module:
#   1. Generates ~/.claude/themes/stylix.json with a full base16 token mapping.
#   2. Sets the theme preference in ~/.claude/settings.json to "custom:stylix".
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
  cfg = config.phillipgreenii.programs.claude-code.theme;
  c = config.lib.stylix.colors;

  # ---------------------------------------------------------------------------
  # base field: Claude Code requires "dark" or "light" as the fallback preset.
  # stylix.polarity drives this. "either" (auto-detect) has no static mapping,
  # so we fall back to "dark" as the conservative default.
  polarity = config.stylix.polarity or "either";
  base = if polarity == "light" then "light" else "dark";

  tokenMap = import ./colors.nix { colors = c; };
in
{
  options.phillipgreenii.programs.claude-code.theme = {
    enable = lib.mkEnableOption "Stylix-aligned Claude Code theme";
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude-code.enable && cfg.enable) {
    assertions = [
      {
        assertion = config.stylix.enable;
        message = ''
          phillipgreenii.programs.claude-code.theme.enable = true requires stylix.enable = true.
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
    phillipgreenii.programs.claude-code.settings.theme = "custom:stylix";
  };
}
