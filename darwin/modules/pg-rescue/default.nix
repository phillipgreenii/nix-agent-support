{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.logSources / dashboardProviders are declared
  # at darwin/system scope in phillipgreenii-nix-support-apps
  # (darwin/modules/observability/registration.nix), so this lives in darwin,
  # not in the home-manager module — setting them from HM targets undeclared
  # options and fails eval (same reasoning as pg-router's and ccpool's
  # registrations).
  obs = config.phillipgreenii.observability;
in
{
  config = lib.mkIf (obs.enable or false) {
    # pg-rescue appends one JSON line per run (including the happy path) to
    # ${XDG_STATE_HOME}/pg-rescue/runs.jsonl (format: packages/pg-rescue/
    # README.md, "Run log").
    #
    # `path` is deliberately NOT set. The logSources submodule's default glob
    # is ${env:XDG_STATE_HOME}/<attr name>/*.jsonl, which for this attribute
    # name is ${XDG_STATE_HOME}/pg-rescue/*.jsonl. That matches runs.jsonl and
    # does not match the rotated runs.jsonl.1, the runs.jsonl.lock rotation
    # lock or anything under runs/, which is correct. (pg-router-events needed
    # an explicit path only because its service name differs from its
    # directory; this one does not.)
    #
    # `format = "raw"`, not the default "jsonl": the jsonl contract
    # (`phillipgreenii-nix-support-apps` ADR 0038) makes the filelog receiver parse `time` as the timestamp and `level` as
    # the severity, and the run log has neither (its timestamp field is `ts`,
    # and a run has a `result`, not a level). Under jsonl every line would
    # raise a timestamp/severity parse error in the collector. Under raw the
    # log body is the verbatim run-log line, stamped with the ingestion time
    # (a few seconds after the run ends); errorAlert defaults to off for raw
    # sources, which is wanted here because a failed handler is a measurement,
    # not a daemon error. The consequences:
    #   - the receiver reads only NEW lines (start_at: end), so history
    #     starts at the first activation that ships this source;
    #   - the dashboard (packages/pg-rescue/grafana/pg-rescue-runs.json)
    #     reads fields out of the raw line with LogQL line filters and a
    #     regexp stage rather than `| json`.
    phillipgreenii.observability.logSources.pg-rescue = {
      format = "raw";
    };

    # Per-chain rate dashboard (bead pg2-lnfwd). Same "Claude Agents" folder
    # title as the pa-monitor and ccpool providers, so all converge on one
    # Grafana folder BY TITLE (no folderUid; see pa-monitor's registration).
    # Only rendered when observability.ui.enable is also set.
    phillipgreenii.observability.dashboardProviders.pg-rescue = {
      folder = "Claude Agents";
      dashboards = [ ../../../packages/pg-rescue/grafana/pg-rescue-runs.json ];
    };
  };
}
