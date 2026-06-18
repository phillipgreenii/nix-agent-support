# tuicr — code-review TUI (https://github.com/agavra/tuicr).
#
# Installs the package and, when Stylix is active, generates a Stylix-aligned
# theme so tuicr matches the rest of the base16-themed system.
#
# Why this module exists
# ----------------------
# tuicr has a capable theming system (config `theme` key + custom themes under
# the config `themes/` dir), but with NO config it falls all the way through its
# resolution chain to `appearance = system`. That picks a built-in theme keyed
# off the OS light/dark setting — entirely unrelated to the Stylix base16 scheme
# driving the terminal — so it renders mismatched / low-contrast ("unreadable").
# This module closes that gap.
#
# What it does (mirrors home/programs/claude-theme + home/programs/atuin):
#   1. Always installs pkgs.llm-agentsPkgs.tuicr.
#   2. When stylix.enable: writes ~/.config/tuicr/themes/stylix.toml (mapping in
#      ./theme.nix) and sets `theme = "stylix"` in ~/.config/tuicr/config.toml.
#      An explicit `theme` wins over `appearance` in tuicr's resolution order.
#
# Standalone-safe: theming is gated on `config.stylix.enable or false`, so a
# consumer that uses agent-support without importing Stylix still evaluates.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.tuicr;

  # Lazy — only forced inside the (cfg.theme.enable && stylixOn) branch below,
  # which can only be true when the Stylix HM module is present.
  themeTokens = import ./theme.nix {
    colors = config.lib.stylix.colors;
    inherit lib;
  };
  stylixOn = config.stylix.enable or false;

  tomlFormat = pkgs.formats.toml { };
in
{
  options.phillipgreenii.programs.tuicr = {
    theme.enable = lib.mkEnableOption "Stylix-aligned tuicr theme" // {
      default = true;
    };

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      description = ''
        Extra keys merged into tuicr's config.toml (e.g. diff_view, mouse,
        leader). When the Stylix theme is active, `theme = "stylix"` is added
        automatically (override it here to pin a different bundled theme).
      '';
      example = {
        diff_view = "side-by-side";
        mouse = true;
      };
    };
  };

  config = lib.mkMerge [
    { home.packages = [ pkgs.llm-agentsPkgs.tuicr ]; }

    # Stylix theme: write the generated theme file and point tuicr at it.
    (lib.mkIf (cfg.theme.enable && stylixOn) {
      phillipgreenii.programs.tuicr.settings.theme = lib.mkDefault "stylix";
      xdg.configFile."tuicr/themes/stylix.toml".source =
        tomlFormat.generate "tuicr-stylix-theme.toml" themeTokens;
    })

    # Render config.toml only when there is something to write, so we never
    # clobber a hand-managed config with an empty file.
    (lib.mkIf (cfg.settings != { }) {
      xdg.configFile."tuicr/config.toml".source = tomlFormat.generate "tuicr-config.toml" cfg.settings;
    })
  ];
}
