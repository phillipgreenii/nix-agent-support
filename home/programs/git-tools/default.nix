{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.git-tools = {
    enable = lib.mkEnableOption "git-tools";
    package = lib.mkPackageOption pkgs "git-tools" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.git-tools.enable {
    home.packages = [ config.phillipgreenii.programs.git-tools.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      git-branch-maintenance = {
        platform = "common";
        source = "${config.phillipgreenii.programs.git-tools.package}/share/tldr/pages.common/git-branch-maintenance.md";
      };
      git-branch-status = {
        platform = "common";
        source = "${config.phillipgreenii.programs.git-tools.package}/share/tldr/pages.common/git-branch-status.md";
      };
      git-choose-branch = {
        platform = "common";
        source = "${config.phillipgreenii.programs.git-tools.package}/share/tldr/pages.common/git-choose-branch.md";
      };
    };
  };
}
