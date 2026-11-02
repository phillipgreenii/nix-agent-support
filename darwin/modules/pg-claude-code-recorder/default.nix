{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.* is declared at darwin/system scope, so the
  # registrations live here and follow the Home Manager flag across
  # `home-manager.users.<u>` (darwin/modules/pg-task-focus and
  # pg-connector-github are the precedents).
  obs = config.phillipgreenii.observability;

  hmUsers = config.home-manager.users or { };
  recorderOf = u: u.phillipgreenii.programs.pg-claude-code-recorder or { };
  daemonUsers = lib.filter (u: (recorderOf u).daemon.enable or false) (lib.attrValues hmUsers);
  daemonDeployed = daemonUsers != [ ];

  # Single-job registrations are one per machine: the first enabled user's
  # settings are read, and the assertion below says so for a second user.
  first = recorderOf (lib.head daemonUsers);

  # Metrics address (recorded choice): the exporter every workspace daemon uses
  # (a loopback Prometheus /metrics endpoint, scraped through metricsTargets).
  # The daemon's exporter is off unless `metrics.addr` is set; the Home Manager
  # module renders it from `metrics.port` (default 49320) while
  # `observability.enable` is on, and the scrape below reads the same option.
  port = first.metrics.port or 49320;

  # The daemon logs JSONL on standard error, which launchd captures into the
  # agent's log directory (home/programs/pg-claude-code-recorder `logDir`).
  suffix = lib.optionalString ((first.instance or "") != "") "-${first.instance}";
  daemonLog = "\${env:XDG_STATE_HOME}/pg-claude-code-recorder-launchd${suffix}/launchd-stderr.log";

  # The control mod's request log, under the shared control state directory.
  controlLog = "${
    first.controlStateDir or "\${env:XDG_STATE_HOME}/pg-claude-control"
  }/requests.jsonl";

  # The rule files and dashboard are created by the alerts packet (7c); the
  # paths are the contract. Registered files are never loaded together: the
  # rules while the daemon is deployed, the deleteRules-only twin otherwise.
  grafanaDir = ../../../packages/pg-claude-code-recorder/grafana;
in
{
  config = lib.mkIf (obs.enable or false) {
    assertions = [
      {
        assertion = builtins.length daemonUsers <= 1;
        message = "phillipgreenii.programs.pg-claude-code-recorder.daemon.enable is set for ${toString (builtins.length daemonUsers)} home-manager users: the metrics target, log sources and alert rules are one registration per machine, so enable the daemon for exactly one user.";
      }
    ];

    phillipgreenii.observability = {
      # The scrape and the log sources exist only while a daemon is deployed;
      # the daemon-down alert reads `up{job="pg-claude-code-recorder"}` and has
      # noDataState Alerting, so it MUST NOT be loaded without the daemon.
      metricsTargets = lib.mkIf daemonDeployed {
        pg-claude-code-recorder = {
          inherit port;
          scrapeInterval = "30s";
        };
      };

      # Absolute single-file paths, no wildcard: the rotated archive and the
      # rotation lock are never matched. errorAlert is off: the dedicated rules
      # carry the service levels and a generic rate rule would double-page.
      logSources = lib.mkIf daemonDeployed {
        pg-claude-code-recorder = {
          path = daemonLog;
          format = "jsonl";
          errorAlert.enable = false;
        };
        pg-claude-control = {
          path = controlLog;
          format = "jsonl";
          errorAlert.enable = false;
        };
      };

      # The rules file while the daemon is deployed; otherwise the
      # deleteRules-only twin, because Grafana keeps a file-provisioned rule
      # live after its file is dropped (pg-connector-github, pg2-susum).
      alertRuleFiles = [
        (
          if daemonDeployed then
            grafanaDir + "/alerting/alerts.yaml"
          else
            grafanaDir + "/alerting/alerts-delete.yaml"
        )
      ];

      dashboardProviders = lib.mkIf daemonDeployed {
        pg-claude-code-recorder = {
          folder = "Claude Agents";
          dashboards = [ (grafanaDir + "/pg-claude-code-recorder.json") ];
        };
      };
    };
  };
}
