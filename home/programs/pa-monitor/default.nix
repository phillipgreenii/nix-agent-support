{
  config,
  options,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pa-monitor;
  # `phillipgreenii.observability` is only declared at darwin/system scope.
  # In HM scope the option doesn't exist; gate emitterEnv resolution on
  # whether the option declaration is visible in this module's evaluation.
  hasObs = options.phillipgreenii ? observability;
  obs = if hasObs then config.phillipgreenii.observability else null;

  # Wrapper script so macOS Background Activity shows `pa-monitor-daemon`
  # instead of the bare hash-prefixed nix-store path.
  daemonWrapper = pkgs.writeShellScriptBin "pa-monitor-daemon" ''
    exec ${cfg.package}/bin/pa-monitor daemon "$@"
  '';

  # Resolve emitter env vars when the observability module is present;
  # otherwise an empty attrset so the LaunchAgent runs without OTel.
  emitterEnv =
    if obs != null && obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pa-monitor";
        protocol = "grpc";
      }
    else
      { };
in
{
  options.phillipgreenii.programs.pa-monitor = {
    enable = lib.mkEnableOption "pa-monitor (per-user Claude agents daemon + TUI)";
    package = lib.mkPackageOption pkgs "pa-monitor" { };

    daemon.enable = lib.mkEnableOption ''
      pa-monitor-daemon LaunchAgent — runs the daemon continuously at
      login. Disabled by default; opt in per host.
    '';
  };

  # Grafana dashboard registration is handled by the parallel darwin module
  # (darwin/modules/pa-monitor) because phillipgreenii.observability
  # .dashboardProviders is declared at darwin scope, not HM scope.
  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home.packages = [ cfg.package ];

    # LaunchAgent only when explicitly enabled and only on darwin.
    launchd.agents.pa-monitor-daemon = lib.mkIf (cfg.daemon.enable && pkgs.stdenv.isDarwin) {
      enable = true;
      config = {
        Label = "com.phillipg.pa-monitor-daemon";
        ProgramArguments = [ "${daemonWrapper}/bin/pa-monitor-daemon" ];
        RunAtLoad = true;
        KeepAlive = true;
        StandardErrorPath = "${config.xdg.stateHome}/pa-monitor/launchd-stderr.log";
        StandardOutPath = "${config.xdg.stateHome}/pa-monitor/launchd-stdout.log";
        EnvironmentVariables = emitterEnv;
      };
    };
  };
}
