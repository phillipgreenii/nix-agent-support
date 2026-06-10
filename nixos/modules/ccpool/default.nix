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
  interval =
    let
      vals = lib.filter (v: v != null) (
        map (u: u.phillipgreenii.programs.ccpool.reap.intervalSeconds or null) (lib.attrValues hmUsers)
      );
    in
    if vals == [ ] then 300 else lib.head vals;
in
{
  config = lib.mkIf reapEnabledByAnyUser {
    systemd.user.services.ccpool-reap = {
      description = "ccpool reap idle/over-cap sessions";
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${pkgs.ccpool}/bin/ccpool reap";
      };
    };
    systemd.user.timers.ccpool-reap = {
      description = "Run ccpool reap periodically";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnUnitActiveSec = interval;
        OnBootSec = interval;
      };
    };
  };
}
