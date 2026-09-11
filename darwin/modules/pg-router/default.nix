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
  obs = config.phillipgreenii.observability;

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
in
{
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.logSources.pg-router = { };
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
          exec ${pkg}/bin/pg-router run
        '';
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pg-router/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pg-router/launchd-stdout.log";
        };
      };
    })
  ];
}
