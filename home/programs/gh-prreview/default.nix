{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.gh-prreview = {
    enable = lib.mkEnableOption "gh-prreview";
    package = lib.mkPackageOption pkgs "gh-prreview" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.gh-prreview.enable {
    home.packages = [ config.phillipgreenii.programs.gh-prreview.package ];

    programs.tldr.customPages.gh-prreview = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${config.phillipgreenii.programs.gh-prreview.package}/share/tldr/pages.common/gh-prreview.md";
    };
  };
}
