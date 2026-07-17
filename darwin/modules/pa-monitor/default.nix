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

  # OTel settings for the shared config file, derived from the same emitterEnv
  # the daemon used to receive via its plist. The ENDPOINT key is present iff
  # obs.enable, so it is the correct gate. We deliberately carry only the
  # endpoint: there is no `protocol` config field (the Go exporters are
  # gRPC-only), OTEL_SERVICE_NAME is set in Go, and the module passes no
  # resourceAttrs so OTEL_RESOURCE_ATTRIBUTES is absent.
  otelSettings = lib.optionalAttrs (emitterEnv ? OTEL_EXPORTER_OTLP_ENDPOINT) {
    otel.endpoint = emitterEnv.OTEL_EXPORTER_OTLP_ENDPOINT;
  };

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
      phillipgreenii.observability = {
        # The dashboard provider AND the alert rule groups are all titled
        # "Claude Agents" so they converge on ONE Grafana folder BY TITLE. Do
        # NOT pin a folderUid: Grafana alerting file-provisioning has no
        # folderUid field and ignores it (grafana/grafana#125079), and alerting
        # provisions BEFORE dashboards — so a pinned dashboard UID just splits
        # the folder in two. Title-only converges: alerting creates the folder
        # (random UID), dashboards reuse it by title. See pg2-h3lr and
        # phillipgreenii-nix-support-apps ADR 0039.
        dashboardProviders.pa-monitor = {
          folder = "Claude Agents";
          dashboards = [ ../../../packages/pa-monitor/grafana/pa-monitor-overview.json ];
        };
        alertRuleFiles = [
          ../../../packages/pa-monitor/grafana/alerting/auth-failure.yaml
          ../../../packages/pa-monitor/grafana/alerting/daemon-connection.yaml
        ];
      };

      # Feed OTel config into each daemon-enabled user's pa-monitor settings via
      # a shared HM module. This reads each user's own daemon.enable and never
      # enumerates config.home-manager.users, avoiding the read-then-write
      # recursion that a mapAttrs-over-users approach would cause.
      home-manager.sharedModules = lib.optional (otelSettings != { }) (
        { config, lib, ... }:
        {
          config = lib.mkIf (config.phillipgreenii.programs.pa-monitor.daemon.enable or false) {
            phillipgreenii.programs.pa-monitor.settings = otelSettings;
          };
        }
      );
    })

    # LaunchAgent registration via the canonical helper (ADR 0049, amended
    # by 0051). The wrapper lands at
    # /nix/var/nix/profiles/system/sw/libexec/pg-launchd/pa-monitor-daemon
    # (stable path, off the user PATH, GC-rooted via the system profile), and the helper
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
          # OTel is now sourced from ~/.config/pa-monitor/config.toml (single
          # source of truth), written from obs via home-manager.sharedModules
          # above — no longer injected as plist env.
        };
      };
    })
  ];
}
