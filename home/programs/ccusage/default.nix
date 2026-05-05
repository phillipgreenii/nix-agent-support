{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.ccusage;
in
{
  options.phillipgreenii.programs.ccusage.enable =
    lib.mkEnableOption "ccusage Claude Code usage analytics CLI";

  config = lib.mkIf cfg.enable {
    home.packages = [ pkgs.llm-agentsPkgs.ccusage ];
  };
}
