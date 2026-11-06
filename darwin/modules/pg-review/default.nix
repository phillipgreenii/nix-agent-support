{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # The tool, its configuration and its router stanzas live in home-manager scope
  # (home/programs/pg-review). The alert-rule registration below is declared at
  # darwin/system scope, so it lives here and follows the HM flag across
  # `home-manager.users.<u>`, exactly as darwin/modules/pg-keep-awake does. The module is
  # inert until some user enables the tool AND the observability stack is on.
  hmUsers = config.home-manager.users or { };
  reviewUsers = lib.filter (u: u.phillipgreenii.programs.pg-review.enable or false) (
    lib.attrValues hmUsers
  );
  gate = (obs.enable or false) && reviewUsers != [ ];
in
{
  # Only alertRuleFiles is registered: the tool PUSHES OTLP gauges (metrics.catalog) and listens
  # on no port, so there is nothing to scrape, writes no event log for a log source, and has no
  # dashboard. The rule group's folder "Host" is created by alerting provisioning BY TITLE
  # (never pin a folderUid: grafana/grafana#125079).
  config = lib.mkIf gate {
    phillipgreenii.observability.alertRuleFiles = [
      ../../../packages/pg-review/grafana/alerting/alerts.yaml
    ];
  };
}
