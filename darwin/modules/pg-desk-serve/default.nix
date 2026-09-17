{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.services.pg-desk-serve;

  # XDG_STATE_HOME default for macOS user agents, matching every other
  # generic services.* launchd module in this repo (darwin/modules/pa-monitor,
  # darwin/modules/pg-router).
  primaryUser = config.system.primaryUser or null;
  stateHome =
    if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pg-desk-serve";
in
{
  # A generic darwin module running `pg-desk serve` as a launchd user agent
  # [design "Human views and operator commands": "a long-lived HTTP server
  # run as a launchd user agent by a generic services.pg-desk-serve darwin
  # module in this repo (package, port, log path options)"]. No
  # organization identifiers appear here — a consuming (ZR) machine flake
  # enables this module and supplies its own soak override [Out of scope:
  # packet 11, phillipg-nix-ziprecruiter].
  options.phillipgreenii.services.pg-desk-serve = {
    enable = lib.mkEnableOption "pg-desk serve as a launchd user agent";

    package = lib.mkPackageOption pkgs "pg-desk" { };

    # The soak option (D18, D27): a SEPARATE mechanism from the config-
    # rendered `serve.addr` (home/programs/pg-desk's own option, defaulting
    # to today's legacy 127.0.0.1:9818 per design section 7.7) — the two
    # are never collapsed into one [Binding decisions]. When soak.enable is
    # true, this module passes `pg-desk serve`'s own `--port` flag
    # (cmd/pg-desk/serve.go: "a convenience alias for --addr ... binding
    # 127.0.0.1"), overriding whatever serve.addr the config file carries,
    # without touching that config file at all. When soak.enable is false,
    # no flag is passed and pg-desk serve falls back to its configured (or
    # legacy-default) serve.addr.
    soak = {
      enable = lib.mkEnableOption ''
        the temporary soak listen port, overriding config's serve.addr via
        `pg-desk serve --port` while `pg-pr sync` still holds the legacy
        port (9818) for both the dashboard and /metrics until the phase 11
        cutover flip [design "Human views and operator commands"; D18,
        D27]. `phillipg-nix-ziprecruiter` (packet 11) enables this in
        Phase 9 and turns it off at the phase 11 flip.
      '';
      port = lib.mkOption {
        type = lib.types.port;
        default = 9819;
        description = ''
          The soak listen port (D27), generic-module default. A consumer
          MAY override this value, but only with a comment saying why —
          never by redefining the option itself [Binding decisions].
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    phillipgreenii.system.launchdServices.userAgents.pg-desk-serve = {
      label = "com.phillipg.pg-desk-serve";
      script = ''
        exec ${cfg.package}/bin/pg-desk serve${lib.optionalString cfg.soak.enable " --port ${toString cfg.soak.port}"}
      '';
      runAtLoad = true;
      keepAlive = true;
      serviceConfig = {
        StandardOutPath = "${stateHome}/pg-desk/launchd-stdout.log";
        StandardErrorPath = "${stateHome}/pg-desk/launchd-stderr.log";
      };
    };
  };
}
