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
  # pr-pool writes its JSONL event log to the standard path
  # ${XDG_STATE_HOME}/pr-pool/events.jsonl, which the default `path` glob
  # (${env:XDG_STATE_HOME}/pr-pool/*.jsonl) already matches — so no overrides are
  # needed. Guarded on obs.enable so it is a no-op on machines without the stack.
  obs = config.phillipgreenii.observability;

  # Read pr-pool.daemon.enable across all HM users; the LaunchAgent gets
  # registered once at system scope when any user opted in — mirrors
  # pa-monitor's own hmUsers pattern. Exactly one daemon-enabled user's
  # config drives the single LaunchAgent instance (scoped to ENABLED users
  # only: repoRoot/configText have no default, so reading them off a
  # daemon-DISABLED user would throw "used but not defined").
  hmUsers = config.home-manager.users or { };
  daemonUsers = lib.filter (u: u.phillipgreenii.programs.pr-pool.daemon.enable or false) (
    lib.attrValues hmUsers
  );
  daemonEnabledByAnyUser = daemonUsers != [ ];
  daemonCfg =
    if daemonEnabledByAnyUser then
      (lib.head daemonUsers).phillipgreenii.programs.pr-pool.daemon
    else
      null;

  pkg = pkgs.pr-pool;

  # XDG_STATE_HOME default for macOS user agents. The launchdServices helper
  # plist runs under the primary user, so resolve via system.primaryUser.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pr-pool";
in
{
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.logSources.pr-pool = { };
    })

    # LaunchAgent registration via the canonical helper (ADR 0049, amended by
    # 0051), mirroring darwin/modules/pa-monitor/default.nix (precedents:
    # pa-monitor, codeburn, ccpool, pg-ccaudit): the HM systemd.user.services
    # daemon unit alone is a darwin no-op (darwin has no systemd), so the
    # daemon submodule needs its own LaunchAgent to actually run there
    # (Task 1.7).
    (lib.mkIf daemonEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.pr-pool-daemon = {
        label = "com.phillipg.pr-pool-daemon";
        script = ''
          export PR_POOL_REPO_ROOT=${lib.escapeShellArg daemonCfg.repoRoot}
          ${lib.optionalString (
            daemonCfg.beadsPrefix != null
          ) "export PR_POOL_BEADS_PREFIX=${lib.escapeShellArg daemonCfg.beadsPrefix}"}
          export PR_POOL_CONFIG=${pkgs.writeText "pr-pool-daemon-config.toml" daemonCfg.configText}
          ${lib.optionalString (
            daemonCfg.gates.quotaPausedPath != null
          ) "export PR_POOL_QUOTA_PAUSED=${lib.escapeShellArg daemonCfg.gates.quotaPausedPath}"}
          ${lib.optionalString (
            daemonCfg.gates.cicdDownPath != null
          ) "export PR_POOL_CICD_DOWN=${lib.escapeShellArg daemonCfg.gates.cicdDownPath}"}
          exec ${pkg}/bin/pr-pool run
        '';
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pr-pool/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pr-pool/launchd-stdout.log";
        };
      };
    })
  ];
}
