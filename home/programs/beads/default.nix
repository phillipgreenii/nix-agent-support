{
  lib,
  config,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.beads;
in
{
  options.phillipgreenii.programs.beads.enable = lib.mkEnableOption "beads issue tracker and dolt";

  config = lib.mkIf cfg.enable {
    home.packages = [
      pkgs.llm-agentsPkgs.beads
      pkgs.unstable.dolt
    ];

    programs.bash.initExtra = lib.mkIf config.phillipgreenii.programs.bash.enable ''
      source <(bd completion bash)
    '';

    programs.zsh.initContent = lib.mkIf config.phillipgreenii.programs.zsh.enable ''
      source <(bd completion zsh)
    '';
  };
}
