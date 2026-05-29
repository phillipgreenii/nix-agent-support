{
  config,
  osConfig ? null,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pa-monitor;
  # `phillipgreenii.observability` is only declared at darwin/system scope.
  # nix-darwin passes the system config to HM modules as `osConfig`, so we
  # read the observability surface from there. When the HM module evaluates
  # outside of nix-darwin (e.g. standalone home-manager), `osConfig` is
  # null and emitterEnv falls back to `{}`.
  obs =
    if osConfig != null && (osConfig.phillipgreenii.observability or null) != null then
      osConfig.phillipgreenii.observability
    else
      null;

  # Stable launchd shim. Lives at ~/.local/bin/pa-monitor-daemon-launcher (a
  # path that NEVER changes across pa-monitor rebuilds) and exec's the current
  # binary via the user nix profile (which home-manager updates atomically).
  #
  # Why this matters: the previous design put the wrapper in /nix/store/<hash>
  # and pointed the LaunchAgent plist directly there. Every pa-monitor rebuild
  # changed the hash → changed the plist → required launchd bootout+bootstrap
  # on every darwin-rebuild. When that sequence stumbled the daemon stayed
  # down and code "didn't deploy."
  #
  # With this design the plist's ProgramArguments is the same string on every
  # rebuild, so launchd never sees a "different plist" and never needs the
  # bootout dance. A `launchctl kickstart -k gui/$UID/com.phillipg.pa-monitor-daemon`
  # is the canonical way to pick up new code after a rebuild.
  daemonLauncherRel = ".local/bin/pa-monitor-daemon-launcher";
  daemonLauncherAbs = "${config.home.homeDirectory}/${daemonLauncherRel}";

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

    # Stable launcher at ~/.local/bin/pa-monitor-daemon-launcher. See the let
    # block above for the rationale.
    home.file.${daemonLauncherRel} = lib.mkIf cfg.daemon.enable {
      text = ''
        #!/bin/sh
        # pa-monitor LaunchAgent shim. Path is stable across pa-monitor
        # rebuilds; exec's the nix-darwin per-user profile binary which IS
        # updated atomically on home-manager activation.
        #
        # Run `launchctl kickstart -k gui/$UID/com.phillipg.pa-monitor-daemon`
        # after a rebuild to pick up new code.
        exec "/etc/profiles/per-user/${config.home.username}/bin/pa-monitor" daemon "$@"
      '';
      executable = true;
    };

    # LaunchAgent only when explicitly enabled and only on darwin.
    launchd.agents.pa-monitor-daemon = lib.mkIf (cfg.daemon.enable && pkgs.stdenv.isDarwin) {
      enable = true;
      config = {
        Label = "com.phillipg.pa-monitor-daemon";
        ProgramArguments = [ daemonLauncherAbs ];
        RunAtLoad = true;
        KeepAlive = true;
        StandardErrorPath = "${config.xdg.stateHome}/pa-monitor/launchd-stderr.log";
        StandardOutPath = "${config.xdg.stateHome}/pa-monitor/launchd-stdout.log";
        EnvironmentVariables = emitterEnv;
      };
    };
  };
}
