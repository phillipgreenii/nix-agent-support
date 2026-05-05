{
  config,
  lib,
  pkgs,
  ...
}:

{
  options.phillipgreenii.programs.agent-activity = {
    enable = lib.mkEnableOption "agent-activity API for AI agent management";
    package = lib.mkPackageOption pkgs "agent-activity" { };
  };

  config = lib.mkIf config.phillipgreenii.programs.agent-activity.enable {
    home.packages = [ config.phillipgreenii.programs.agent-activity.package ];
  };
}
