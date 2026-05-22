{ config, lib, ... }:
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
  # Wired unconditionally: the dashboards.nix module only renders when
  # observability.enable AND observability.ui.enable, so it's a no-op on
  # machines without the observability stack.
  config = lib.mkIf (obs.enable or false) {
    phillipgreenii.observability.dashboardProviders.pa-monitor = {
      folder = "Claude Agents";
      dashboards = [ ../../../packages/pa-monitor/grafana/pa-monitor-overview.json ];
    };
  };
}
