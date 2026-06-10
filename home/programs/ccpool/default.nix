{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.ccpool;
  tomlFormat = pkgs.formats.toml { };
in
{
  options.phillipgreenii.programs.ccpool = {
    enable = lib.mkEnableOption "ccpool (Claude Code session pool manager)";
    package = lib.mkPackageOption pkgs "ccpool" { };

    reap.enable = lib.mkEnableOption ''
      the ccpool-reap timer (LaunchAgent on darwin / systemd user timer on
      linux) that runs `ccpool reap` periodically. The unit itself is
      registered by the parallel darwin/nixos module (per ADR 0049); this is
      the public-facing enable flag.
    '';
    reap.intervalSeconds = lib.mkOption {
      type = lib.types.int;
      default = 300;
      description = "How often (seconds) to run `ccpool reap`.";
    };

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      description = "Contents of ccpool's config.toml (merged over plugin_dir).";
      example = {
        pool.max_sessions = 6;
        notify.adapter = "desktop";
      };
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home.packages = [ cfg.package ];

    # config.toml: the module always pins plugin_dir to the rendered store path;
    # everything else comes from cfg.settings.
    xdg.configFile."ccpool/config.toml".source = tomlFormat.generate "ccpool-config.toml" (
      lib.recursiveUpdate cfg.settings {
        claude.plugin_dir = "${cfg.package}/share/ccpool-plugin";
      }
    );
  };
}
