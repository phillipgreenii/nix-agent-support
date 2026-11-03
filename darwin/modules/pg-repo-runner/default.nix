{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # The runner kernel, its configuration and its roles live in home-manager
  # scope (home/programs/pg-repo-runner). The alert rule and dashboard
  # registrations are declared at darwin/system scope, so they live here and
  # follow the HM flag across `home-manager.users.<u>`, the way
  # darwin/modules/pg-keep-awake does. Inert until some user enables the
  # runner AND the observability stack is on.
  hmUsers = config.home-manager.users or { };
  runnerUsers = lib.filter (u: u.phillipgreenii.programs.pg-repo-runner.enable or false) (
    lib.attrValues hmUsers
  );
  gate = (obs.enable or false) && runnerUsers != [ ];
in
{
  # Only alertRuleFiles and dashboardProviders are registered. The runner PUSHES
  # OTLP gauges (through ccpool emit-gauge) and listens on no port, so there is
  # no metricsTargets entry, and it writes no jsonl log for a Loki source. The
  # rule group and the dashboard provider are both titled "Host" so they
  # converge on one Grafana folder BY TITLE (never pin a folderUid:
  # grafana/grafana#125079). The rule uids must also be registered with
  # pg-router-probe in the deployment repo.
  config = lib.mkIf gate {
    phillipgreenii.observability = {
      alertRuleFiles = [ ../../../packages/pg-repo-runner/grafana/alerting/alerts.yaml ];
      dashboardProviders.pg-repo-runner = {
        folder = "Host";
        dashboards = [ ../../../packages/pg-repo-runner/grafana/pg-repo-runner.json ];
      };
    };
  };
}
