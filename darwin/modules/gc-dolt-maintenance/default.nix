{
  config,
  lib,
  pkgs,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  pkg = pkgs.gc-dolt-maintenance;

  # OTel emitter env for the scheduled agent. Resolved here (darwin scope) where
  # the observability surface is declared; passed to the helper which merges it
  # with PG_LAUNCHD_WRAPPER under EnvironmentVariables.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "gc-dolt-maintenance";
        protocol = "http/protobuf";
      }
    else
      { };

  primaryUser = config.system.primaryUser or null;
in
{
  # Register gc-dolt-maintenance's Grafana dashboard as a dashboardProvider on
  # the workspace observability stack. Lives in darwin (not HM) per ADR 0049 /
  # the same pattern as pa-monitor — setting from HM would target an undeclared
  # option and fail eval.
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.dashboardProviders.dolt-maintenance = {
        folder = "Claude Agents";
        dashboards = [ ../../../packages/gc-dolt-maintenance/grafana/dolt-maintenance-overview.json ];
      };
    })

    # Hourly LaunchAgent via the canonical helper (ADR 0049). Fires at minute 0
    # of every hour via StartCalendarInterval; runAtLoad and keepAlive are both
    # false so the service exits cleanly between runs.
    #
    # healthCheck is disabled: the service is not expected to be in state=running
    # after activation — it only runs at its scheduled time.
    (lib.mkIf (primaryUser != null) {
      phillipgreenii.system.launchdServices.userAgents.gc-dolt-maintenance = {
        label = "com.phillipg.gc-dolt-maintenance";
        script = ''
          exec ${pkg}/bin/gc-dolt-maintenance --city /Users/${primaryUser}/gc
        '';
        runAtLoad = false;
        keepAlive = false;
        healthCheck = false;
        serviceConfig = {
          StartCalendarInterval = [ { Minute = 0; } ];
          StandardOutPath = "/Users/${primaryUser}/.gc/dolt-maintenance.log";
          StandardErrorPath = "/Users/${primaryUser}/.gc/dolt-maintenance.log";
          EnvironmentVariables = emitterEnv;
        };
      };
    })
  ];
}
