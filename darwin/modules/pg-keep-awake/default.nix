{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # The keeper, its configuration and its LaunchAgent live in home-manager scope
  # (home/programs/pg-keep-awake, the HM-scoped launchd pattern of
  # `phillipgreenii-nix-personal` ADR 0055). The alert-rule registration below
  # is declared at darwin/system scope (phillipgreenii-nix-support-apps), so it
  # lives here and follows the HM flag across `home-manager.users.<u>`, the way
  # darwin/modules/pg-task-focus does. The module is inert until some user
  # enables the keeper AND the observability stack is on.
  hmUsers = config.home-manager.users or { };
  keeperUsers = lib.filter (u: u.phillipgreenii.programs.pg-keep-awake.enable or false) (
    lib.attrValues hmUsers
  );
  gate = (obs.enable or false) && keeperUsers != [ ];
in
{
  # Only alertRuleFiles is registered (bead pg2-8bgxg.5). The other three
  # registrations pg-task-focus makes are deliberately absent:
  #  - metricsTargets: the keeper PUSHES OTel (design section 12) and listens on
  #    no port, so there is nothing to scrape; its rules read the pushed series.
  #  - logSources: the keeper writes no *.jsonl event log under XDG_STATE_HOME
  #    (launchd only captures stdout/stderr in `pg-keep-awake-launchd/`, and its
  #    logs go to OTel), so a Loki log source would match nothing.
  #  - dashboardProviders: no dashboard is designed.
  # The rule group's folder "Keep Awake" is created by alerting provisioning
  # BY TITLE (never pin a folderUid: Grafana ignores it, grafana/grafana#125079).
  config = lib.mkIf gate {
    phillipgreenii.observability.alertRuleFiles = [
      ../../../packages/pg-keep-awake/grafana/alerting/alerts.yaml
    ];
  };
}
