{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.pg-disk-reclaimer = {
    enable = lib.mkEnableOption "pg-disk-reclaimer";
    package = lib.mkPackageOption pkgs "pg-disk-reclaimer" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.pg-disk-reclaimer.enable {
    home.packages = [ config.phillipgreenii.programs.pg-disk-reclaimer.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      pg-disk-reclaimer = {
        platform = "common";
        source = "${config.phillipgreenii.programs.pg-disk-reclaimer.package}/share/tldr/pages.common/pg-disk-reclaimer.md";
      };
    };
  };
}
