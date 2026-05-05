{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pw-reset-agents;
in
{
  options.phillipgreenii.programs.pw-reset-agents.enable =
    lib.mkEnableOption "pw-reset-agents — stop all waiting agents";
  config = lib.mkIf cfg.enable {
    home.packages = [ pkgs.pw-reset-agents ];
  };
}
