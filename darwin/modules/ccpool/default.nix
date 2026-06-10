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
in
{
  config = lib.mkIf reapEnabledByAnyUser {
    phillipgreenii.system.launchdServices.userAgents.ccpool-reap = {
      label = "com.phillipg.ccpool-reap";
      script = ''
        exec ${pkg}/bin/ccpool reap
      '';
      runAtLoad = true;
      # `ccpool reap` is a periodic short task (StartInterval), not a long-running
      # daemon — it does its work and exits. keepAlive defaults to true in the
      # helper, which would make launchd RESTART it on every exit (a ~10s respawn
      # loop). Disable keepAlive so StartInterval is the only re-trigger (runs at
      # load, then every `interval` seconds), and exempt it from the health check
      # (which expects state=running, which a one-shot never reaches).
      keepAlive = false;
      healthCheck = false;
      serviceConfig = {
        StartInterval = interval; # the periodic re-trigger
      };
    };
  };
}
