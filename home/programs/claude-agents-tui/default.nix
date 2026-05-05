{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.claude-agents-tui = {
    enable = lib.mkEnableOption "claude-agents-tui";
    package = lib.mkPackageOption pkgs "claude-agents-tui" { };
  };

  config =
    lib.mkIf
      (
        config.phillipgreenii.programs.claude.enable
        && config.phillipgreenii.programs.claude-agents-tui.enable
      )
      {
        home.packages = [ config.phillipgreenii.programs.claude-agents-tui.package ];
      };
}
