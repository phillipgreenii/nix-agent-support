{
  config,
  lib,
  pkgs,
  ...
}:
let
  hmUsers = config.home-manager.users or { };
  ingestUsers = lib.filter (u: u.phillipgreenii.programs.claude-tool-tracker.ingest.enable or false) (
    lib.attrValues hmUsers
  );
  ingestEnabledByAnyUser = ingestUsers != [ ];

  interval = lib.head (
    (map (u: u.phillipgreenii.programs.claude-tool-tracker.ingest.intervalSeconds or 300) ingestUsers)
    ++ [ 300 ]
  );

  pkg = pkgs.claude-tool-tracker;

  primaryUser = config.system.primaryUser or null;
  stateHome =
    if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/claude-tool-tracker";
in
{
  # The scheduled ingest, as a nix-declared launchd USER agent registered through the
  # canonical `phillipgreenii.system.launchdServices.userAgents` helper, mirroring
  # darwin/modules/pg-ccaudit. The tracker's hooks only append spool files under
  # $XDG_STATE_HOME/claude-tool-tracker/events; tracker.db (the source of
  # `claude-extended-tool-approver report --source tracker`) is written ONLY by
  # `claude-tool-tracker ingest`. Before this agent existed nothing ran ingest, so the
  # database went stale at the last manual run while event files kept arriving.
  #
  # Health check: the agent's stdout log holds one `total` line per run, and
  # tracker.db's mtime must stay within a few intervals of the newest event file.
  config = lib.mkIf ingestEnabledByAnyUser {
    phillipgreenii.system.launchdServices.userAgents.claude-tool-tracker-ingest = {
      label = "com.phillipg.claude-tool-tracker-ingest";
      script = ''
        # ingest prints one line per event file (thousands); keep only the `total` line so
        # the append-only log grows by one line per run. The exit status is preserved.
        rc=0
        out=$(${pkg}/bin/claude-tool-tracker ingest) || rc=$?
        printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(printf '%s\n' "$out" | tail -n 1)"
        exit "$rc"
      '';
      runAtLoad = true;
      # A periodic one-shot: StartInterval is the only re-trigger and the process
      # exits, so keepAlive and the state=running health check do not apply.
      keepAlive = false;
      healthCheck = false;
      serviceConfig = {
        StartInterval = interval;
        # Without logs a failing ingest would be silent and the database would go stale
        # while every report kept answering from it.
        StandardErrorPath = "${stateHome}/claude-tool-tracker/ingest.err.log";
        StandardOutPath = "${stateHome}/claude-tool-tracker/ingest.out.log";
      };
    };
  };
}
