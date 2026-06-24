{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pa-monitor;
  tomlFormat = pkgs.formats.toml { };
in
{
  options.phillipgreenii.programs.pa-monitor = {
    enable = lib.mkEnableOption "pa-monitor (per-user Claude agents daemon + TUI)";
    package = lib.mkPackageOption pkgs "pa-monitor" { };

    daemon.enable = lib.mkEnableOption ''
      pa-monitor-daemon LaunchAgent — runs the daemon continuously at
      login. Disabled by default; opt in per host.

      The LaunchAgent itself is registered via the canonical
      `phillipgreenii.system.launchdServices.userAgents` helper from
      darwin/modules/pa-monitor/default.nix (system scope). This HM
      option only exists as the public-facing enable flag; the darwin
      module reads it across `config.home-manager.users.<u>`.
    '';

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = {
        otel.endpoint = "http://127.0.0.1:4317";
      };
      description = ''
        Written to `~/.config/pa-monitor/config.toml`. Keys must match
        pa-monitor's TOML schema (e.g. `otel.endpoint`,
        `otel.resource_attributes`, `plan_tier`, `[[decorator]]`). When empty,
        no file is written and pa-monitor uses its built-in defaults. The file
        is rendered whenever the program (`enable`) **or** the daemon
        (`daemon.enable`) is enabled, so a daemon-only host still gets its
        config.
      '';
    };
  };

  # Grafana dashboard registration and LaunchAgent wiring both live in the
  # parallel darwin module (darwin/modules/pa-monitor) because the relevant
  # options are declared at darwin/system scope, not HM scope.
  config = lib.mkMerge [
    (lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
      home.packages = [ cfg.package ];
    })
    # config.toml is the daemon's single source of OTel settings. Render it
    # whenever settings were supplied AND the full program OR the daemon is
    # enabled — decoupled from `claude.enable && enable` so a daemon-only host
    # (the LaunchAgent gate in darwin/modules/pa-monitor is daemon.enable-only)
    # still gets its config file. Intentionally NOT coupled to home.packages:
    # the LaunchAgent runs the daemon from the Nix store path, so the binary
    # need not be on the user's PATH.
    (lib.mkIf
      (
        cfg.settings != { }
        && ((config.phillipgreenii.programs.claude.enable && cfg.enable) || cfg.daemon.enable)
      )
      {
        xdg.configFile."pa-monitor/config.toml".source =
          tomlFormat.generate "pa-monitor-config.toml" cfg.settings;
      }
    )
  ];
}
