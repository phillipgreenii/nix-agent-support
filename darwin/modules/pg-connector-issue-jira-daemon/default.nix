{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.{logSources,alertRuleFiles} are declared at
  # darwin/system scope in phillipgreenii-nix-support-apps
  # (darwin/modules/observability), so this lives in darwin, not in the
  # home-manager module -- setting them from HM targets undeclared options and
  # fails eval (same reasoning as pg-connector-github's registration).
  obs = config.phillipgreenii.observability;

  # The daemon, its config and its LaunchAgent live in home-manager scope
  # (home/programs/pg-connector, option `jira.enable`, off by default). The
  # alert rules are registered here at darwin scope, so they follow that flag
  # across `home-manager.users.<u>` (same pattern as darwin/modules/pg-connector-github).
  hmUsers = config.home-manager.users or { };
  daemonDeployed = lib.any (u: u.phillipgreenii.programs.pg-connector.jira.enable or false) (
    lib.attrValues hmUsers
  );
in
{
  # The Jira daemon's observability registration (docket pg2-2qyyh.2, design
  # "7. Observability"): its three log files and its alert rules. This module
  # sits BESIDE darwin/modules/pg-connector-issue-jira (the old stateless
  # backend's module and alert file), which stays untouched and installed until
  # a later cleanup; the two share no attribute name and no path.
  #
  # The module is a no-op on a machine without the observability stack. The log
  # sources are harmless while the daemon is not installed (the log files do not
  # exist), so they follow `obs.enable` alone. The ALERT RULES do NOT: the
  # daemon-down rule has noDataState Alerting, so with no daemon installed it
  # fires forever (pg2-bry0w). They are therefore registered only when some
  # home-manager user sets `phillipgreenii.programs.pg-connector.jira.enable`,
  # the option that installs the daemon.
  config = lib.mkIf (obs.enable or false) {
    # The daemon writes three JSONL files into
    # $XDG_STATE_HOME/pg-connector-issue-jira-daemon/ (Go constants
    # config.StateDirName, config.EventLogFileName, config.DaemonLogFileName and
    # config.ClientLogFileName in packages/pg-connector/cmd/pg-connector-issue-jira/
    # internal/config/defaults.go; the flake check pins them). The state
    # directory is deliberately NOT the old backend's
    # $XDG_STATE_HOME/pg-connector-issue-jira, so none of these paths can match
    # the old backend's event log.
    #
    # Every entry sets `path` to ONE literal file name: no wildcard, hence no
    # match for the rotated archive (<file>.1) or the rotation lock
    # (<file>.lock), and no file matched by two entries (the default glob would
    # match all three live files and label the client log's rows with the
    # daemon's service_name).
    #
    # The entry keyed `pg-connector-issue-jira-daemon` carries the event log;
    # the structured log shares its service_name (the Loki selector the alert
    # rules read) through a second entry with an explicit serviceName; the
    # client log has its own service_name so a client failure is never mistaken
    # for a daemon one.
    #
    # errorAlert (the generic "Daemon Health" error-rate rule, generated PER
    # ENTRY from its serviceName) is switched OFF for all three entries: the
    # dedicated rules file carries the service levels (store-aside reads the
    # error-level store_refused line, auth/unavailable/rate-limited read the
    # failing calls, daemon-down covers an unreachable daemon, which is the
    # client log's usual error), so a generic rate rule would page the same
    # incident twice, and two entries on one serviceName would generate a
    # duplicate rule.
    phillipgreenii.observability = {
      logSources = {
        pg-connector-issue-jira-daemon = {
          path = "\${env:XDG_STATE_HOME}/pg-connector-issue-jira-daemon/events.jsonl";
          errorAlert.enable = false;
        };
        pg-connector-issue-jira-daemon-log = {
          path = "\${env:XDG_STATE_HOME}/pg-connector-issue-jira-daemon/daemon.log.jsonl";
          serviceName = "pg-connector-issue-jira-daemon";
          errorAlert.enable = false;
        };
        pg-connector-issue-jira-daemon-client = {
          path = "\${env:XDG_STATE_HOME}/pg-connector-issue-jira-daemon/client.log.jsonl";
          serviceName = "pg-connector-issue-jira-daemon-client";
          errorAlert.enable = false;
        };
      };

      # Group pg-connector-issue-jira-daemon, folder pg-connector. The old
      # backend's issue-jira-alerts.yaml is deliberately NOT listed here (its
      # module still registers it). Listed only while the daemon is deployed
      # (see the header comment); otherwise the deleteRules file below.
      alertRuleFiles =
        if daemonDeployed then
          [ ../../../packages/pg-connector/grafana/alerting/pg-connector-issue-jira-daemon-alerts.yaml ]
        else
          # Grafana never drops a file-provisioned rule just because its file
          # disappeared (pg2-susum): while the daemon is not deployed, register
          # a deleteRules-only file for the daemon rules' uids instead.
          [
            ../../../packages/pg-connector/grafana/alerting/pg-connector-issue-jira-daemon-alerts-delete.yaml
          ];
    };
  };
}
