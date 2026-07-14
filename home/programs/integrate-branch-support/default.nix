{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.integrate-branch-support;
in
{
  options.phillipgreenii.programs.integrate-branch-support = {
    enable = lib.mkEnableOption "integrate-branch-support CLI";
    package = lib.mkPackageOption pkgs "integrate-branch-support" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages.integrate-branch-support = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/integrate-branch-support.md";
    };
  };
}
