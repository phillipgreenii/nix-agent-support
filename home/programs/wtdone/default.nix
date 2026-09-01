{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.wtdone = {
    enable = lib.mkEnableOption "wtdone";
    package = lib.mkPackageOption pkgs "wtdone" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.wtdone.enable {
    home.packages = [ config.phillipgreenii.programs.wtdone.package ];

    programs.tldr.customPages.wtdone = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${config.phillipgreenii.programs.wtdone.package}/share/tldr/pages.common/wtdone.md";
    };
  };
}
