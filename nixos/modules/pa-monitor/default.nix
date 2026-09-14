{
  config,
  lib,
  pkgs,
  ...
}:
let
  # Read pa-monitor.daemon.enable across all HM users, same aggregation
  # pattern as darwin/modules/pa-monitor and nixos/modules/ccpool: the
  # systemd --user unit is registered once at system scope when any user
  # opted in.
  hmUsers = config.home-manager.users or { };
  daemonEnabledByAnyUser = lib.any (u: u.phillipgreenii.programs.pa-monitor.daemon.enable or false) (
    lib.attrValues hmUsers
  );

  pkg = pkgs.pa-monitor;
in
{
  config = lib.mkIf daemonEnabledByAnyUser {
    systemd.user.services.pa-monitor-daemon = {
      description = "pa-monitor daemon (per-user Claude agents monitor)";
      wantedBy = [ "default.target" ];
      serviceConfig = {
        ExecStart = "${pkg}/bin/pa-monitor daemon";
        Restart = "always";
      };
    };
  };
}
