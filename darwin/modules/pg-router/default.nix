{
  config,
  lib,
  pkgs,
  ...
}:
let
  # phillipgreenii.observability.logSources is declared at darwin/system scope in
  # phillipgreenii-nix-support-apps (darwin/modules/observability/registration.nix),
  # so this lives in darwin, not in the home-manager module — setting it from HM
  # targets an undeclared option and fails eval (same reasoning as pa-monitor's
  # dashboardProviders registration).
  #
  # pg-router writes its JSONL event log to the standard path
  # ${XDG_STATE_HOME}/pg-router/events.jsonl, which the default `path` glob
  # (${env:XDG_STATE_HOME}/pg-router/*.jsonl) already matches — so no overrides are
  # needed. Guarded on obs.enable so it is a no-op on machines without the stack.
  #
  # Relabeled to `pg-router-events` (observability design decision D3): the
  # NEW OTLP log push this module now also wires (emitterEnv below) claims
  # the plain `pg-router` service_name for pg-router's own operational
  # WARN/ERROR slog lines, so this pre-existing registration — which only
  # ever carried the dispatch-outcome ledger (events.jsonl), a narrower
  # signal — moves to its own label rather than colliding with it. The
  # glob/path is UNCHANGED: events.jsonl itself is untouched as a file,
  # only its Loki label moves.
  obs = config.phillipgreenii.observability;

  # OTel OTLP log-export env for the daemon (design section 5.4), mirroring
  # pa-monitor's/ollama's own darwin module precedent for
  # `obs.mkEmitterEnv` exactly: resolved here (darwin scope, where the
  # observability surface is declared) and merged into the LaunchAgent's
  # EnvironmentVariables below. `obs ? mkEmitterEnv` guards against a
  # narrower eval fixture (this repo's own flake.nix checks) that stubs
  # `logSources` but declares no such helper.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pg-router";
        protocol = "grpc";
      }
    else
      { };

  # Read pg-router.daemon.enable across all HM users; the LaunchAgent gets
  # registered once at system scope when any user opted in — mirrors
  # pa-monitor's own hmUsers pattern. Exactly one daemon-enabled user's
  # config drives the single LaunchAgent instance (scoped to ENABLED users
  # only: repoRoot/configText have no default, so reading them off a
  # daemon-DISABLED user would throw "used but not defined").
  hmUsers = config.home-manager.users or { };
  daemonUsers = lib.filter (u: u.phillipgreenii.programs.pg-router.daemon.enable or false) (
    lib.attrValues hmUsers
  );
  daemonEnabledByAnyUser = daemonUsers != [ ];
  daemonCfg =
    if daemonEnabledByAnyUser then
      (lib.head daemonUsers).phillipgreenii.programs.pg-router.daemon
    else
      null;

  pkg = pkgs.pg-router;

  # XDG_STATE_HOME default for macOS user agents. The launchdServices helper
  # plist runs under the primary user, so resolve via system.primaryUser.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pg-router";

  # The primary user's nix-darwin/home-manager profile bin dir (bead
  # pg2-gt3ju). A launchd UserAgent's process starts from launchd's own bare
  # default PATH (`/usr/bin:/bin:/usr/sbin:/sbin` — confirmed live via
  # `launchctl print`), which never includes this profile — unlike an
  # interactive login shell, which always has it. pg-router's own package
  # wrapper (packages/pg-router/default.nix) already threads ccpool/bd/pg-pr/
  # jq/pg-connector/pg-connector-*'s own bin dirs onto PATH (bead pg2-sh024),
  # but deliberately does NOT and MUST NOT wrap in `gh` there: per
  # packages/pg-connector/pg-connector-pr-github.nix's own comment and the
  # pg2-n75tk boundary ("Core must not know how another tool is configured"),
  # `gh` is provisioned once, generically, by this machine's home-manager
  # profile — exactly where an interactive shell already finds it — not
  # per-consumer inside pg-router's or pg-connector's own wrapProgram. This
  # gives the daemon's own process the SAME profile bin dir an interactive
  # shell has, so the nested exec chain (pg-router -> pg-router-source-pg-
  # connector -> pg-connector -> pg-connector-pr-github -> `gh`, each hop an
  # ambient-$PATH exec inheriting its parent's env unmodified) can resolve
  # `gh` the same way it resolves any other profile-provisioned CLI a future
  # backend might need — naming no specific tool here, only the profile
  # location itself.
  hmProfileBin = if primaryUser != null then "/etc/profiles/per-user/${primaryUser}/bin" else null;
in
{
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.logSources.pg-router-events = {
        serviceName = "pg-router-events";
      };
    })

    # LaunchAgent registration via the canonical helper (ADR 0049, amended by
    # 0051), mirroring darwin/modules/pa-monitor/default.nix (precedents:
    # pa-monitor, codeburn, ccpool, pg-ccaudit): the HM systemd.user.services
    # daemon unit alone is a darwin no-op (darwin has no systemd), so the
    # daemon submodule needs its own LaunchAgent to actually run there
    # (Task 1.7).
    (lib.mkIf daemonEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.pg-router-daemon = {
        label = "com.phillipg.pg-router-daemon";
        script = ''
          ${lib.optionalString (
            hmProfileBin != null
          ) ''export PATH=${lib.escapeShellArg hmProfileBin}:"$PATH"''}
          export PG_ROUTER_REPO_ROOT=${lib.escapeShellArg daemonCfg.repoRoot}
          ${lib.optionalString (
            daemonCfg.beadsPrefix != null
          ) "export PG_ROUTER_BEADS_PREFIX=${lib.escapeShellArg daemonCfg.beadsPrefix}"}
          export PG_ROUTER_CONFIG=${pkgs.writeText "pg-router-daemon-config.toml" daemonCfg.configText}
          ${lib.optionalString (
            daemonCfg.gates.operatorPausedPath != null
          ) "export PG_ROUTER_OPERATOR_PAUSED=${lib.escapeShellArg daemonCfg.gates.operatorPausedPath}"}
          ${lib.optionalString (
            daemonCfg.gates.cicdDownPath != null
          ) "export PG_ROUTER_CICD_DOWN=${lib.escapeShellArg daemonCfg.gates.cicdDownPath}"}
          ${lib.optionalString (
            daemonCfg.handlerCommand != null
          ) "export PG_ROUTER_HANDLER_COMMAND=${lib.escapeShellArg daemonCfg.handlerCommand}"}
          ${lib.optionalString (
            daemonCfg.handlerCommandDir != null
          ) "export PG_ROUTER_HANDLER_COMMAND_DIR=${lib.escapeShellArg daemonCfg.handlerCommandDir}"}
          ${lib.optionalString (
            daemonCfg.handlerConfig != null
          ) "export PG_ROUTER_CCPOOL_HANDLER_CONFIG=${lib.escapeShellArg daemonCfg.handlerConfig}"}
          ${lib.optionalString (
            daemonCfg.handlerCcpoolPool != null
          ) "export CCPOOL_POOL=${lib.escapeShellArg daemonCfg.handlerCcpoolPool}"}
          ${lib.optionalString (
            daemonCfg.metricsAddr != null
          ) "export PG_ROUTER_METRICS_ADDR=${lib.escapeShellArg daemonCfg.metricsAddr}"}
          exec ${pkg}/bin/pg-router run
        '';
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pg-router/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pg-router/launchd-stdout.log";
          # OTLP log-export env (design section 5.4, D6) — empty when
          # obs.enable is false or the helper is absent (emitterEnv's own
          # guard above), so this is a no-op merge on a machine without the
          # observability stack.
          EnvironmentVariables = emitterEnv;
        };
      };
    })
  ];
}
