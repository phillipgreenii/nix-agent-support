{
  config,
  lib,
  pkgs,
  ...
}:
let
  # Read pg-router-ccpool-handler.daemon.enable across all HM users; the
  # LaunchAgent gets registered once at system scope when any user opted in
  # -- mirrors darwin/modules/pg-router's own hmUsers pattern (which in turn
  # mirrors pa-monitor's). Exactly one daemon-enabled user's config drives
  # the single LaunchAgent instance (scoped to ENABLED users only:
  # socket/id have no default, so reading them off a daemon-DISABLED user
  # would throw "used but not defined"). `periodicDrain` has no darwin-side
  # equivalent yet, matching darwin/modules/pg-router's own precedent
  # ("periodicDrain has no darwin-side equivalent yet -- a darwin deployment
  # that wants the timer-driven form still needs its own launchd timer
  # wiring, or should use daemon instead").
  hmUsers = config.home-manager.users or { };
  daemonUsers = lib.filter (
    u: u.phillipgreenii.programs.pg-router-ccpool-handler.daemon.enable or false
  ) (lib.attrValues hmUsers);
  daemonEnabledByAnyUser = daemonUsers != [ ];
  daemonCfg =
    if daemonEnabledByAnyUser then
      (lib.head daemonUsers).phillipgreenii.programs.pg-router-ccpool-handler.daemon
    else
      null;

  # handlerCommandDir (this bead, pg2-pteab): re-exposes the HM module's own
  # `roles`-derived `handlerCommandDir` output at darwin scope, the same way
  # daemonCfg above re-reads socket/id/self/token -- but keyed on `.enable`,
  # not `.daemon.enable`: handlerCommandDir has nothing to do with the
  # register-LaunchAgent flow above (it is populated whenever the HM module
  # is enabled at all, independent of periodicDrain/daemon), so scoping it to
  # daemon-enabled users only would wrongly hide it from a deployment that
  # enables this module purely for its `roles`/`handlerCommandDir` output.
  # Lets a sibling darwin module (darwin/modules/pg-router) wire it into its
  # own PG_ROUTER_HANDLER_COMMAND_DIR export without reaching back into
  # `home-manager.users.<name>` itself.
  enabledUsers = lib.filter (u: u.phillipgreenii.programs.pg-router-ccpool-handler.enable or false) (
    lib.attrValues hmUsers
  );
  handlerCommandDir =
    if enabledUsers != [ ] then
      (lib.head enabledUsers).phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir
    else
      null;

  # poolMetrics (bead pg2-mr0sl): the ONE HM option group in this module
  # that gets a REAL darwin mirror (unlike periodicDrain/daemon's own
  # freeform systemd-string intervals, "no darwin-side equivalent yet")
  # because `intervalSeconds` is a plain integer -- see that HM option's
  # own doc comment for why. `script` is the SAME rendered
  # mkPoolMetricsScript derivation the HM module's own systemd timer runs
  # (re-exposed via the HM module's own readOnly `poolMetrics.script`
  # output, mirroring how `handlerCommandDir` is re-exposed just below),
  # so darwin never re-derives the `--pool <name>=<dir>` argv independently.
  poolMetricsUsers = lib.filter (
    u: u.phillipgreenii.programs.pg-router-ccpool-handler.poolMetrics.enable or false
  ) (lib.attrValues hmUsers);
  poolMetricsEnabledByAnyUser = poolMetricsUsers != [ ];
  poolMetricsCfg =
    if poolMetricsEnabledByAnyUser then
      (lib.head poolMetricsUsers).phillipgreenii.programs.pg-router-ccpool-handler.poolMetrics
    else
      null;

  pkg = pkgs.pg-router-ccpool-handler;

  # XDG_STATE_HOME default for macOS user agents. The launchdServices helper
  # plist runs under the primary user, so resolve via system.primaryUser --
  # same rationale as darwin/modules/pg-router's own stateHome.
  primaryUser = config.system.primaryUser or null;
  stateHome =
    if primaryUser != null then
      "/Users/${primaryUser}/.local/state"
    else
      "/tmp/pg-router-ccpool-handler";
in
{
  options.phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir = lib.mkOption {
    type = lib.types.nullOr lib.types.package;
    readOnly = true;
    # Deliberately NO `default` here: a readOnly option's own `default` (if
    # any) counts as one of its `evalOptionValue` definitions
    # (nixpkgs `lib/modules.nix`), so combining `default` with an
    # UNCONDITIONAL `config`-set value below (this module always sets it,
    # never behind an `mkIf`) trips "read-only, but it's set multiple
    # times" even though there is only one real assignment. `config` always
    # provides a value (`null` or the real derivation) below, so no default
    # is needed — matches the HM module's own `handlerCommandDir` option,
    # which has no `default` for the same reason.
    description = ''
      Darwin-scope mirror of the HM module's own
      `phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir`
      output (the joined directory of per-role JSON files rendered from
      `roles`) -- re-exposed here the same way this module already re-reads
      `daemonCfg.{socket,id,self,token}` above, so a sibling darwin module
      (e.g. `darwin/modules/pg-router`) can wire it into its own
      `PG_ROUTER_HANDLER_COMMAND_DIR` export without reaching back into
      `home-manager.users.<name>` itself. `null` when no HM user has this
      module enabled.
    '';
  };

  config = lib.mkMerge [
    { phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir = handlerCommandDir; }
    (lib.mkIf daemonEnabledByAnyUser {
      # LaunchAgent registration via the canonical helper (ADR 0049, amended by
      # 0051), mirroring darwin/modules/pg-router/default.nix's own daemon
      # LaunchAgent (precedents: pa-monitor, codeburn, ccpool, pg-ccaudit,
      # pg-router): the HM systemd.user.services daemon unit alone is a darwin
      # no-op (darwin has no systemd), so this module's own daemon submodule
      # needs its own LaunchAgent to actually run there (Task 5.12; ADR 0065's
      # "Operator surface" section).
      phillipgreenii.system.launchdServices.userAgents.pg-router-ccpool-handler-daemon = {
        label = "com.phillipg.pg-router-ccpool-handler-daemon";
        script = ''
          exec ${pkg}/bin/pg-router-ccpool-handler register \
            --socket ${lib.escapeShellArg daemonCfg.socket} \
            --id ${lib.escapeShellArg daemonCfg.id} \
            --self ${lib.escapeShellArg daemonCfg.self} \
            ${lib.optionalString (daemonCfg.token != null) "--token ${lib.escapeShellArg daemonCfg.token}"}
        '';
        runAtLoad = true;
        # keepAlive = false (unlike pg-router's own daemon LaunchAgent, which
        # uses keepAlive = true for its genuinely long-running `run`): `register`
        # is a one-shot request/reply that exits 0 on success, so relaunching it
        # on every clean exit would just spam re-registrations. runAtLoad above
        # still re-announces this participant on every login/boot.
        keepAlive = false;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pg-router-ccpool-handler/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pg-router-ccpool-handler/launchd-stdout.log";
        };
      };
    })
    (lib.mkIf poolMetricsEnabledByAnyUser {
      # poolMetrics LaunchAgent (bead pg2-mr0sl): the periodic pool-capacity
      # pass, via the SAME canonical helper the daemon LaunchAgent above
      # uses. `script` runs the HM module's own rendered
      # `poolMetrics.script` derivation directly (no argv to reconstruct
      # here) -- keepAlive = false + StartInterval, the SAME
      # "periodic short task, not a long-running daemon" shape
      # darwin/modules/pg-ccaudit's own sweep agent already established
      # (see that module's own doc comment for the keepAlive=false
      # rationale: launchd would otherwise treat every clean exit as a
      # crash to respawn).
      phillipgreenii.system.launchdServices.userAgents.pg-router-ccpool-handler-pool-metrics = {
        label = "com.phillipg.pg-router-ccpool-handler-pool-metrics";
        script = ''
          exec ${poolMetricsCfg.script}
        '';
        runAtLoad = true;
        keepAlive = false;
        healthCheck = false; # a one-shot never reaches state=running (pg-ccaudit precedent)
        serviceConfig = {
          StartInterval = poolMetricsCfg.intervalSeconds;
          StandardErrorPath = "${stateHome}/pg-router-ccpool-handler/pool-metrics-launchd-stderr.log";
          StandardOutPath = "${stateHome}/pg-router-ccpool-handler/pool-metrics-launchd-stdout.log";
        };
      };
    })
  ];
}
