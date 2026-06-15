{
  config,
  lib,
  pkgs,
  ...
}:
let
  hmUsers = config.home-manager.users or { };
  reapEnabledByAnyUser = lib.any (u: u.phillipgreenii.programs.ccpool.reap.enable or false) (
    lib.attrValues hmUsers
  );

  # Pick the interval from the first user that set one (default 300).
  interval =
    let
      vals = lib.filter (v: v != null) (
        map (u: u.phillipgreenii.programs.ccpool.reap.intervalSeconds or null) (lib.attrValues hmUsers)
      );
    in
    if vals == [ ] then 300 else lib.head vals;

  pkg = pkgs.ccpool;

  # XDG_STATE_HOME default for the reap agent's launchd logs. The plist runs
  # under the primary user; mirror pa-monitor's resolution via system.primaryUser.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/ccpool";
in
{
  config = lib.mkIf reapEnabledByAnyUser {
    phillipgreenii.system.launchdServices.userAgents.ccpool-reap = {
      label = "com.phillipg.ccpool-reap";
      script = ''
        exec ${pkg}/bin/ccpool reap-all
      '';
      runAtLoad = true;
      # `ccpool reap-all` is a periodic short task (StartInterval), not a long-running
      # daemon — it does its work and exits. keepAlive defaults to true in the
      # helper, which would make launchd RESTART it on every exit (a ~10s respawn
      # loop). Disable keepAlive so StartInterval is the only re-trigger (runs at
      # load, then every `interval` seconds), and exempt it from the health check
      # (which expects state=running, which a one-shot never reaches).
      keepAlive = false;
      healthCheck = false;
      serviceConfig = {
        StartInterval = interval; # the periodic re-trigger
        # Surface runtime failures: the agent is keepAlive-off and health-check
        # exempt, so without logs a crashing reap run would be silent. launchd
        # creates the parent dir (~/.local/state/ccpool) if it is missing.
        # Mirrors pa-monitor's stateHome logging pattern.
        StandardErrorPath = "${stateHome}/ccpool/reap.err.log";
        StandardOutPath = "${stateHome}/ccpool/reap.out.log";
      };
    };
  };
}
