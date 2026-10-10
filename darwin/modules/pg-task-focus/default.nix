{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # The daemon, its configuration and its LaunchAgent live in home-manager scope
  # (home/programs/pg-task-focus, the HM-scoped launchd pattern of
  # `phillipgreenii-nix-personal` ADR 0055). The four registrations below are
  # declared at darwin/system scope (phillipgreenii-nix-support-apps), so they
  # live here and follow the HM flag across `home-manager.users.<u>`, the way
  # darwin/modules/pg-desk-serve reads its HM user. The module is inert until
  # some user enables the daemon AND the observability stack is on.
  hmUsers = config.home-manager.users or { };
  daemonUsers = lib.filter (u: u.phillipgreenii.programs.pg-task-focus.daemon.enable or false) (
    lib.attrValues hmUsers
  );
  gate = (obs.enable or false) && daemonUsers != [ ];

  # The scrape port is the daemon's listen_port (the first enabled user's: a
  # second user running the daemon on the same machine would need its own port,
  # which this single-job registration cannot express, so the assertion below
  # says so).
  port = (lib.head daemonUsers).phillipgreenii.programs.pg-task-focus.listenPort;
in
{
  config = lib.mkIf gate {
    assertions = [
      {
        assertion = builtins.length daemonUsers == 1;
        message = "phillipgreenii.programs.pg-task-focus.daemon.enable is set for ${toString (builtins.length daemonUsers)} home-manager users: the metrics target, log source and alert rules are one registration per machine, so enable the daemon for exactly one user.";
      }
    ];

    phillipgreenii.observability = {
      # Without a metricsTargets entry nothing is scraped and every alert is
      # dead. The job name is the attribute name, `pg-task-focus`, which the
      # alert rules' `up{job="pg-task-focus"}` reads. The daemon answers a
      # scrape of 127.0.0.1:<port> because that is in its Host allowlist.
      metricsTargets.pg-task-focus = {
        inherit port;
        scrapeInterval = "30s";
      };

      # The daemon writes JSON lines on stdout, which launchd captures into
      # ${XDG_STATE_HOME}/pg-task-focus/pg-task-focus.jsonl: the default glob
      # (${XDG_STATE_HOME}/<name>/*.jsonl) matches it, and the rotated archive
      # and the stderr file are not matched. Loki labels are service_name and
      # level only; ids, routes and event types stay fields. errorAlert's
      # threshold is the smallest the option allows (1): one Error line is
      # already worth a look, and a startup failure writes exactly one (the
      # daemon-down alert covers a daemon that refuses to start as well).
      logSources.pg-task-focus = {
        format = "jsonl";
        errorAlert.threshold = 1;
      };

      # The dashboard provider and the alert rule groups are both titled
      # "Focus" so they converge on ONE Grafana folder BY TITLE (never pin a
      # folderUid: Grafana alerting provisioning ignores it, grafana/grafana
      # #125079, and alerting provisions BEFORE dashboards).
      dashboardProviders.pg-task-focus = {
        folder = "Focus";
        dashboards = [ ../../../packages/pg-task-focus/grafana/pg-task-focus.json ];
      };
      alertRuleFiles = [ ../../../packages/pg-task-focus/grafana/alerting/alerts.yaml ];
    };
  };
}
