{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pa-monitor;
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
  };

  # Grafana dashboard registration and LaunchAgent wiring both live in the
  # parallel darwin module (darwin/modules/pa-monitor) because the relevant
  # options are declared at darwin/system scope, not HM scope. Per ADR 0049
  # the LaunchAgent MUST go through phillipgreenii.system.launchdServices —
  # writing launchd.agents directly from this HM module is forbidden.
  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home.packages = [ cfg.package ];
  };
}
