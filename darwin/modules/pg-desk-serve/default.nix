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

  # launchd's own stdout/stderr capture paths, hoisted so serviceConfig and
  # manageLogs.files below share one spelling (pg2-jujm3: never retype a path).
  launchdStdoutLog = "${stateHome}/pg-desk/launchd-stdout.log";
  launchdStderrLog = "${stateHome}/pg-desk/launchd-stderr.log";

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

  # pg2-fdtvv: the enabled user's serve.log override, if any (same
  # cross-module read pattern as pgDeskConfigSource just above). Falls back
  # to cmd/pg-desk/serve.go's own defaultServeLogPathSuffix
  # (~/Library/Logs/pg-desk-serve.log) when unset -- the exact default the
  # `pg-desk serve` binary itself uses when serve.log is null
  # (openServeLogFile). serve writes real slog.NewTextHandler records here
  # (packages/pg-desk/cmd/pg-desk/serve.go's telemetry.Fanout call) in
  # ADDITION to its direct OTLP log push, so unlike pa-monitor-daemon/
  # pg-desk-serve's own OTel-only siblings, there IS a real file worth a
  # logSources entry.
  pgDeskServeLogOverride =
    if pgDeskUsers != [ ] then
      (lib.head pgDeskUsers).phillipgreenii.programs.pg-desk.serve.log
    else
      null;
  pgDeskServeLogPath =
    if pgDeskServeLogOverride != null then
      pgDeskServeLogOverride
    else if primaryUser != null then
      "/Users/${primaryUser}/Library/Logs/pg-desk-serve.log"
    else
      # path's type is a plain (non-nullable) string -- fall back to the same
      # /tmp shape stateHome above uses when primaryUser is unresolved,
      # rather than passing null through to the logSources submodule.
      "/tmp/pg-desk-serve/pg-desk-serve.log";

  # pg2-t92xc: the pg-router config `serve` reads (as a file, on every scrape)
  # for the poll interval behind pg_desk_sweep_bound_violated{type,tier}.
  # pg-router's daemon config is DECLARATIVE: it exists only as the store path
  # pkgs.writeText renders from the enabled user's
  # phillipgreenii.programs.pg-router.daemon.configText (darwin/modules/
  # pg-router/default.nix; no .pg-router/config.toml or xdg file is written
  # anywhere). writeText is content-addressed by (name, text), so rendering the
  # SAME name and text here yields the byte-identical store path pg-router's
  # own PG_ROUTER_CONFIG points at -- the file pg-router actually runs on, with
  # no second copy to drift. It cannot go stale: the path is spelled into this
  # service's wrapper text (via the --router-config flag below), so a router
  # config change changes the wrapper hash and the next switch restarts serve
  # onto the new path (same mechanism as pgDeskConfigSource above). The literal
  # name below MUST stay in step with darwin/modules/pg-router/default.nix.
  routerDaemonUsers = lib.filter (u: u.phillipgreenii.programs.pg-router.daemon.enable or false) (
    lib.attrValues hmUsers
  );
  derivedRouterConfig =
    if routerDaemonUsers != [ ] then
      toString (
        pkgs.writeText "pg-router-daemon-config.toml" (lib.head routerDaemonUsers)
        .phillipgreenii.programs.pg-router.daemon.configText
      )
    else
      null;
  routerConfigPath = if cfg.routerConfig != null then cfg.routerConfig else derivedRouterConfig;
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

    routerConfig = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "/path/to/pg-router/config.toml";
      description = ''
        Path of the pg-router config, passed to `pg-desk serve --router-config`.
        `serve` re-reads it as a file on every scrape to learn the router poll
        interval behind the `pg_desk_sweep_bound_violated{type,tier}` metric
        (and so the `pg-desk-sweep-bound-violated` alert). Without it the
        metric emits no series and a violated sweep sizing bound never alerts.

        When `null` (the default) the path is derived from the pg-router daemon
        of an enabled home-manager user
        (`phillipgreenii.programs.pg-router.daemon.configText`, rendered to the
        same store path pg-router's own launchd service is pointed at). If no
        user enables that daemon either, no flag is passed. Set this option
        only to point at a config that lives somewhere else; use a stable path,
        since the file is read on every scrape.
      '';
    };

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
    phillipgreenii = {
      system.launchdServices.userAgents.pg-desk-serve = {
        label = "com.phillipg.pg-desk-serve";
        script = ''
          exec ${cfg.package}/bin/pg-desk serve${lib.optionalString cfg.soak.enable " --port ${toString cfg.soak.port}"}${
            lib.optionalString (
              routerConfigPath != null
            ) " --router-config ${lib.escapeShellArg routerConfigPath}"
          }
        '';
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          EnvironmentVariables =
            emitterEnv
            // lib.optionalAttrs (pgDeskConfigSource != null) {
              PG_DESK_CONFIG = toString pgDeskConfigSource;
            };
          StandardOutPath = launchdStdoutLog;
          StandardErrorPath = launchdStderrLog;
        };
        # pg2-jujm3: rotate the launchd capture logs AND serve's self-written
        # log (pgDeskServeLogPath, which honors the HM serve.log override).
        # Setting `files` REPLACES the option's default list (the two launchd
        # paths), so all three are listed. serve.go opens its log with
        # O_APPEND, so the wrapper's copy-then-truncate rotation is safe; the
        # logSources raw filelog target below is an exact path, so the
        # pg-desk-serve.log.1 archive is not matched.
        manageLogs = {
          enable = true;
          files = [
            launchdStdoutLog
            launchdStderrLog
            pgDeskServeLogPath
          ];
        };
      };

      observability = {
        # pg2-02n5o: liveness/stale-snapshot/sync-failure-rate alert rules for
        # the pg_desk_* metric catalog (see the file's own header for the
        # incident context and the folder-convergence mechanism). Gated on this
        # module's own `cfg.enable`, mirroring pg-router's darwin module's
        # `obs.enable`-only gate for its own alertRuleFiles entry.
        alertRuleFiles = [
          ../../../packages/pg-desk/grafana/alerting/alerts.yaml
        ];

        # pg2-fdtvv: `path` MUST be set explicitly -- serve.log is a plain-text
        # slog.NewTextHandler stream (packages/pg-desk/cmd/pg-desk/serve.go), not
        # the ADR 0038 JSONL contract the option's own default glob assumes, and
        # it lives under ~/Library/Logs, not ${env:XDG_STATE_HOME}/pg-desk-serve/.
        # Set unconditionally on cfg.enable (no separate obs.enable gate), same
        # convention as this module's own alertRuleFiles entry just above --
        # both are "safe to set when observability is disabled" per the option's
        # own doc.
        logSources.pg-desk-serve = {
          path = pgDeskServeLogPath;
          format = "raw";
        };
      };
    };
  };
}
