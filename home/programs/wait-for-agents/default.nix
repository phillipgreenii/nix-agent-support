{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.wait-for-agents = {
    enable = lib.mkEnableOption "wait-for-agents script";
    package = lib.mkPackageOption pkgs "wait-for-agents" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.wait-for-agents.enable {
    home.packages = [ config.phillipgreenii.programs.wait-for-agents.package ];

    programs.tldr.customPages.wait-for-agents-to-finish = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${config.phillipgreenii.programs.wait-for-agents.package}/share/tldr/pages.common/wait-for-agents-to-finish.md";
    };
  };
}
