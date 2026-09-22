{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.services.pg-desk-serve;

  # OTel emitter env for serve (design section 9, D6): resolved here
  # (darwin scope, where the observability surface — declared in
  # phillipgreenii-nix-support-apps — lives) and merged into the
  # LaunchAgent's EnvironmentVariables, mirroring pa-monitor's own
  # obs.mkEmitterEnv call pattern and pg-router's darwin module (D6). Guarded
  # defensively (`obs ? mkEmitterEnv`) the same way pa-monitor's own module
  # is: this repo does not declare phillipgreenii-nix-support-apps as a
  # flake input, so the option only exists once a consuming machine flake
  # imports both.
  obs = config.phillipgreenii.observability;
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pg-desk-serve";
        protocol = "grpc";
      }
    else
      { };

  # XDG_STATE_HOME default for macOS user agents, matching every other
  # generic services.* launchd module in this repo (darwin/modules/pa-monitor,
  # darwin/modules/pg-router).
  primaryUser = config.system.primaryUser or null;
  stateHome =
    if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pg-desk-serve";

  # Cross-module reach into home-manager scope (mirrors darwin/modules/
  # pg-router-ccpool-handler's own hmUsers pattern): pg-desk's config.yaml is
  # rendered by phillipgreenii.programs.pg-desk (home-manager scope,
  # home/programs/pg-desk/default.nix), not by this darwin module. Reading
  # the enabled user's rendered xdg.configFile source here and threading it
  # into PG_DESK_CONFIG below means any config content change produces a new
  # content-addressed store path, which changes this service's generated
  # wrapper text, which changes the wrapper derivation's hash -- exactly what
  # makes `phillipgreenii-nix-personal` ADR 0049's plist-hash comparison
  # bootout/bootstrap the daemon on the next darwin-rebuild switch. Without
  # this, a config-only change (no package rebuild) never touches the
  # wrapper and the running daemon silently keeps serving stale config until
  # someone manually restarts it (observed 2026-09-18, pg2-b0rj4:
  # agentTrackerBackend landed and applied to disk, but pg-desk-serve kept
  # failing "2 backends registered" for two hours because nothing restarted
  # it).
  hmUsers = config.home-manager.users or { };
  pgDeskUsers = lib.filter (u: u.phillipgreenii.programs.pg-desk.enable or false) (
    lib.attrValues hmUsers
  );
  # null when no HM user has phillipgreenii.programs.pg-desk enabled -- falls
  # back to pg-desk's own default config resolution ($XDG_CONFIG_HOME then
  # ~/.config), same as before this change.
  pgDeskConfigSource =
    if pgDeskUsers != [ ] then
      (lib.head pgDeskUsers).xdg.configFile."pg-desk/config.yaml".source
    else
      null;
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
        EnvironmentVariables =
          emitterEnv
          // lib.optionalAttrs (pgDeskConfigSource != null) {
            PG_DESK_CONFIG = toString pgDeskConfigSource;
          };
        StandardOutPath = "${stateHome}/pg-desk/launchd-stdout.log";
        StandardErrorPath = "${stateHome}/pg-desk/launchd-stderr.log";
      };
    };

    # pg2-02n5o: liveness/stale-snapshot/sync-failure-rate alert rules for
    # the pg_desk_* metric catalog (see the file's own header for the
    # incident context and the folder-convergence mechanism). Gated on this
    # module's own `cfg.enable`, mirroring pg-router's darwin module's
    # `obs.enable`-only gate for its own alertRuleFiles entry.
    phillipgreenii.observability.alertRuleFiles = [
      ../../../packages/pg-desk/grafana/alerting/alerts.yaml
    ];
  };
}
