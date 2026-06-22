{
  config,
  lib,
  pkgs,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # Read pa-monitor.daemon.enable across all HM users; the LaunchAgent gets
  # registered once at system scope when any user opted in. The launchdServices
  # helper runs the agent under the primary user (gui/<primary-uid>/<label>).
  hmUsers = config.home-manager.users or { };
  daemonEnabledByAnyUser = lib.any (u: u.phillipgreenii.programs.pa-monitor.daemon.enable or false) (
    lib.attrValues hmUsers
  );

  pkg = pkgs.pa-monitor;

  # OTel emitter env for the daemon. Resolved here (darwin scope) where the
  # observability surface is declared; passed to the helper which merges it
  # with PG_LAUNCHD_WRAPPER under EnvironmentVariables.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pa-monitor";
        protocol = "grpc";
      }
    else
      { };

  # XDG_STATE_HOME default for macOS user agents. The launchdServices helper
  # plist runs under the primary user, so resolve via system.primaryUser.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pa-monitor";
in
{
  # Register pa-monitor's Grafana dashboard as a dashboardProvider on the
  # workspace observability stack. The option `phillipgreenii.observability
  # .dashboardProviders` is declared at darwin/system scope (in
  # phillipgreenii-nix-support-apps), so this lives in darwin, not in the
  # home-manager module — setting it from HM would target an undeclared
  # option and fail eval.
  #
  # Wired unconditionally: the dashboards.nix module only renders when
  # observability.enable AND observability.ui.enable, so it's a no-op on
  # machines without the observability stack.
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.dashboardProviders.pa-monitor = {
        folder = "Claude Agents";
        dashboards = [ ../../../packages/pa-monitor/grafana/pa-monitor-overview.json ];
      };
      phillipgreenii.observability.alertRuleFiles = [
        ../../../packages/pa-monitor/grafana/alerting/auth-failure.yaml
      ];
    })

    # LaunchAgent registration via the canonical helper (ADR 0049). The
    # wrapper lands at /nix/var/nix/profiles/system/sw/bin/pa-monitor-daemon
    # (stable path, GC-rooted via the system profile), and the helper
    # automatically embeds PG_LAUNCHD_WRAPPER = wrapper.outPath so plist
    # hash compares trigger nix-darwin to bootout+bootstrap whenever
    # pa-monitor (and thus the wrapper) changes.
    (lib.mkIf daemonEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.pa-monitor-daemon = {
        label = "com.phillipg.pa-monitor-daemon";
        script = ''
          exec ${pkg}/bin/pa-monitor daemon "$@"
        '';
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pa-monitor/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pa-monitor/launchd-stdout.log";
          EnvironmentVariables = emitterEnv;
        };
      };
    })
  ];
}
