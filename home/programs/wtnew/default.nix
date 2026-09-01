{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.wtnew = {
    enable = lib.mkEnableOption "wtnew";
    package = lib.mkPackageOption pkgs "wtnew" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.wtnew.enable {
    home.packages = [ config.phillipgreenii.programs.wtnew.package ];

    programs.tldr.customPages.wtnew = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${config.phillipgreenii.programs.wtnew.package}/share/tldr/pages.common/wtnew.md";
    };
  };
}
