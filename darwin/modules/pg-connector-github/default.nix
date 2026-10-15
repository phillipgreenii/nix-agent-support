{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.{logSources,alertRuleFiles,dashboardProviders}
  # are declared at darwin/system scope in phillipgreenii-nix-support-apps
  # (darwin/modules/observability), so this lives in darwin, not in the
  # home-manager module -- setting them from HM targets undeclared options and
  # fails eval (same reasoning as pg-connector-pr-github's registration).
  obs = config.phillipgreenii.observability;
in
{
  # Phase 4a of the pg-connector-github daemon program (docket pg2-z5fax.6,
  # design: docs/superpowers/specs/2026-10-09-pg-connector-github-daemon-design.md,
  # "Observability" and "Nix packaging"): registers the daemon's log files, its
  # alert rules and its dashboard with the observability stack. This module sits
  # BESIDE darwin/modules/pg-connector-pr-github, which stays untouched until
  # Phase 6b so a rollback is still observed; the two share no attribute name.
  #
  # Gated on `obs.enable` alone: a no-op on a machine without the observability
  # stack, and harmless while the daemon is idle or not yet installed (the log
  # files do not exist and the rules see no data).
  config = lib.mkIf (obs.enable or false) {
    # The daemon writes three JSONL files into $XDG_STATE_HOME/pg-connector-github/
    # (Go constants config.EventLogFileName, config.DaemonLogFileName and
    # config.ClientLogFileName in packages/pg-connector/cmd/pg-connector-github/
    # internal/config/defaults.go; the flake check pins the names against them).
    # The default logSources glob <state dir>/*.jsonl would match all three live
    # files and label the client log's rows with the daemon's service_name, so
    # every entry sets `path` to ONE literal file name: no wildcard, hence no
    # match for the rotated archive (<file>.1) or the rotation lock (<file>.lock),
    # and no file matched by two entries. The collector's include takes a single
    # glob, so one entry per file is the straightforward mapping.
    #
    # Naming: the entry keyed `pg-connector-github` (the plain service name, like
    # pg-connector-pr-github's) carries the event log. The structured log shares
    # its service_name (Loki selector service_name="pg-connector-github", which
    # the alert rule pg-conn-github-store-aside reads), through a second entry
    # with an explicit serviceName. The client log has its own service_name so a
    # client failure is never mistaken for a daemon one.
    #
    # `format` stays the default "jsonl" (ADR 0038 fields: time as the
    # timestamp, lowercase level as the severity).
    #
    # errorAlert (the generic "Daemon Health" error-rate rule, generated PER
    # ENTRY from its serviceName, uid from the entry name) is switched OFF for all
    # three entries:
    #   - pg-connector-github: the store_refused line of daemon.log.jsonl is
    #     written at error level and the dedicated Loki rule
    #     pg-conn-github-store-aside selects the same service_name, so the
    #     generic rule would page the same incident twice (the reason the
    #     pr-github module switches its own off). It would also be an entry-name
    #     duplicate of the structured-log entry's rule for one serviceName.
    #   - pg-connector-github-daemon-log: same serviceName as above, so leaving
    #     it on would generate a second identical rule for that serviceName.
    #   - pg-connector-github-client: client errors are per-invocation failures
    #     (most often "daemon unreachable"), which the daemon-down rule of the
    #     rules file already pages; a count-only rate rule would double-page that
    #     incident and cannot tell it apart from a single bad request.
    # So no two entries generate a generic alert for one serviceName (none
    # generate one), and the dedicated rules file carries the service levels.
    phillipgreenii.observability = {
      logSources = {
        pg-connector-github = {
          path = "\${env:XDG_STATE_HOME}/pg-connector-github/events.jsonl";
          errorAlert.enable = false;
        };
        pg-connector-github-daemon-log = {
          path = "\${env:XDG_STATE_HOME}/pg-connector-github/daemon.log.jsonl";
          serviceName = "pg-connector-github";
          errorAlert.enable = false;
        };
        pg-connector-github-client = {
          path = "\${env:XDG_STATE_HOME}/pg-connector-github/client.log.jsonl";
          serviceName = "pg-connector-github-client";
          errorAlert.enable = false;
        };
      };

      # Group pg-connector-github, folder pg-connector: six Prometheus rules over
      # the daemon's own metrics and one Loki rule over daemon.log.jsonl.
      # pr-github-alerts.yaml is deliberately NOT listed (it belongs to the old
      # module). Surfaces in the SwiftBar menu through pg-connector-alert-grafana,
      # which lists every firing Grafana alert.
      alertRuleFiles = [
        ../../../packages/pg-connector/grafana/alerting/pg-connector-github-alerts.yaml
      ];

      # Same folder title as the alert group ("pg-connector"), so dashboards and
      # alerts converge on one Grafana folder BY TITLE (no folderUid anywhere).
      # Only rendered when observability.ui.enable is also set.
      dashboardProviders.pg-connector-github = {
        folder = "pg-connector";
        dashboards = [ ../../../packages/pg-connector/grafana/pg-connector-github.json ];
      };
    };
  };
}
