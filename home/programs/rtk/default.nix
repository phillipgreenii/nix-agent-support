{
  config,
  lib,
  pkgs,
  ...
}:
{
  options.phillipgreenii.programs.rtk = {
    enable = lib.mkEnableOption "rtk (Rust Token Killer) - LLM token optimizer";
  };

  config = lib.mkIf config.phillipgreenii.programs.rtk.enable {
    home.packages = [ pkgs.llm-agentsPkgs.rtk ];
  };
}
