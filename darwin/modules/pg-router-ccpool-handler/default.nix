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
  config = lib.mkIf daemonEnabledByAnyUser {
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
  };
}
