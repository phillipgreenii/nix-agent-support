{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # The scheduled job lives in home-manager scope (home/programs/pg-disk-reclaimer);
  # the log source is a system-scope option, so it follows the HM flag across
  # `home-manager.users.<u>` (the pg-repo-runner pattern).
  hmUsers = config.home-manager.users or { };
  autoUsers = lib.filter (
    u:
    (u.phillipgreenii.programs.pg-disk-reclaimer.enable or false)
    && (u.phillipgreenii.programs.pg-disk-reclaimer.auto.enable or false)
  ) (lib.attrValues hmUsers);

  # The scheduled run is one JSONL line per auto.intervalSeconds. Every auto
  # user MUST use the same interval (asserted below); that value I drives the
  # log source's emitInterval and the error alert's window, so the alert is
  # reachable for any interval (the NSA reachability check needs
  # window / interval > threshold, and 3 x I / I = 3 > 1).
  intervals = lib.unique (
    map (u: u.phillipgreenii.programs.pg-disk-reclaimer.auto.intervalSeconds) autoUsers
  );
  interval = if intervals == [ ] then 900 else lib.head intervals;
in
{
  # The run log is JSON lines on stdout (auto.jsonl under
  # $XDG_STATE_HOME/pg-disk-reclaimer), so the default glob of the log source
  # matches it and not the stderr file. The alert is an ERROR line (free space
  # below the lowest threshold). The source's default error alert needs more
  # than 10 lines in 10 minutes, which one line per run can never produce, so
  # the source declares its emit interval and a reachable alert instead: it
  # fires when the ERROR count over three intervals is above 1 (two ERROR lines,
  # not necessarily consecutive: 45 minutes at 900 s), held for the rule's own
  # `for`.
  config = lib.mkIf ((obs.enable or false) && autoUsers != [ ]) {
    assertions = lib.optional ((obs.enable or false) && autoUsers != [ ]) {
      assertion = lib.length intervals <= 1;
      message = "pg-disk-reclaimer: every user with auto.enable MUST use the same auto.intervalSeconds (got ${
        lib.concatMapStringsSep ", " toString intervals
      }); the log source's emit interval and error-alert window derive from it.";
    };

    phillipgreenii.observability.logSources.pg-disk-reclaimer = {
      format = "jsonl";
      emitInterval = "${toString interval}s";
      errorAlert = {
        threshold = 1;
        window = "${toString (3 * interval)}s";
        severity = "critical";
      };
    };

    # Reclaimer dashboard (free space with the auto thresholds, freed bytes,
    # outcomes, skips). Same folder title as the other Claude Agents providers,
    # so all converge on one Grafana folder BY TITLE. Only rendered when
    # observability.ui.enable is also set, and gated like the log source.
    phillipgreenii.observability.dashboardProviders.pg-disk-reclaimer = {
      folder = "Claude Agents";
      dashboards = [ ../../../packages/pg-disk-reclaimer/grafana/pg-disk-reclaimer.json ];
    };
  };
}
