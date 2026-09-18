{
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.osx-bridge-api = {
    enable = lib.mkEnableOption ''
      osx-bridge-api-daemon LaunchAgent — runs the shared macOS integration
      daemon (Calendar via EventKit today; design: "New standalone daemon
      package: owns all macOS TCC-gated OS integration... deployed as a
      launchd user agent so it has its own clean TCC identity") continuously
      at login. Disabled by default; opt in per host.

      This tool has no interactive CLI surface — the daemon mode is its only
      mode — so, unlike pa-monitor/pg-router's own split `enable` (full
      program) vs `daemon.enable` (LaunchAgent) options, one flag covers
      both here.

      The LaunchAgent itself is registered via the canonical
      `phillipgreenii.system.launchdServices.userAgents` helper from
      darwin/modules/osx-bridge-api/default.nix (system scope). This HM
      option only exists as the public-facing enable flag; the darwin
      module reads it across `config.home-manager.users.<u>`.
    '';

    package = lib.mkPackageOption pkgs "osx-bridge-api" { };
  };

  # No home.packages / config file, deliberately: this daemon takes no
  # user-facing settings in v1 (its only configuration is the socket path,
  # threaded in by the darwin module's LaunchAgent script — see
  # darwin/modules/osx-bridge-api/default.nix) and is never invoked
  # directly by a human (design: "Why a shared daemon, not per-backend
  # direct access" — running inside an interactive terminal session is
  # exactly the TCC problem this daemon exists to avoid). The `enable`
  # option above exists solely for the darwin module (where
  # `phillipgreenii.system.launchdServices.userAgents` is actually
  # declared) to read across every HM user, mirroring pa-monitor's/
  # pg-router's own split.
}
