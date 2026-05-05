{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pw-agent-activity;
in
{
  options.phillipgreenii.programs.pw-agent-activity.enable =
    lib.mkEnableOption "pw-agent-activity — wait for all agents to finish";
  config = lib.mkIf cfg.enable {
    home.packages = [ pkgs.pw-agent-activity ];
  };
}
