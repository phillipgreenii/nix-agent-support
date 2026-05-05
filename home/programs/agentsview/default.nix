{
  lib,
  config,
  pkgs,
  ...
}:
{
  options.phillipgreenii.programs.agentsview = {
    enable = lib.mkEnableOption "agentsview";
  };

  config =
    lib.mkIf (pkgs.stdenv.hostPlatform.isDarwin && config.phillipgreenii.programs.agentsview.enable)
      {
        home.packages = [ pkgs.llm-agentsPkgs.agentsview ];
      };
}
