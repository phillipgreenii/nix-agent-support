{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.wtaudit = {
    enable = lib.mkEnableOption "wtaudit";
    package = lib.mkPackageOption pkgs "wtaudit" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.wtaudit.enable {
    home.packages = [ config.phillipgreenii.programs.wtaudit.package ];

    programs.tldr.customPages.wtaudit = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${config.phillipgreenii.programs.wtaudit.package}/share/tldr/pages.common/wtaudit.md";
    };
  };
}
