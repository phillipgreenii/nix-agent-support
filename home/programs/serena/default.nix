{
  config,
  lib,
  inputs,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.serena;
in
{
  options.phillipgreenii.programs.serena.enable =
    lib.mkEnableOption "Serena MCP server for semantic code intelligence";

  config = lib.mkIf cfg.enable {
    home.packages = [ inputs.serena.packages.${pkgs.system}.serena ];
  };
}
