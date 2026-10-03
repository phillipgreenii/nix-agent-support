{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;
in
{
  # Register pa-monitor's Grafana dashboard as a dashboardProvider on the
  # workspace observability stack. The option `phillipgreenii.observability
  # .dashboardProviders` is declared at darwin/system scope (in
  # phillipgreenii-nix-support-apps), so this lives in darwin, not in the
  # home-manager module — setting it from HM would target an undeclared
  # option and fail eval.
  #
  # The pa-monitor-daemon LaunchAgent itself, and its OTel config.toml
  # settings, moved to home/programs/pa-monitor (HM scope, via
  # `phillipgreenii.programs.launchdServices.userAgents`; personal ADR 0055).
  # This module keeps ONLY the system-scope Grafana wiring.
  #
  # Gated on observability.enable: the dashboards.nix module only renders when
  # observability.enable AND observability.ui.enable, so it's a no-op on
  # machines without the observability stack.
  config = lib.mkIf (obs.enable or false) {
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
        ../../../packages/pa-monitor/grafana/alerting/usage-limits.yaml
      ];
    };
  };
}
