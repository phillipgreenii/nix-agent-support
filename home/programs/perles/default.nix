{
  config,
  lib,
  pkgs,
  ...
}:
{
  options.phillipgreenii.programs.perles.enable =
    lib.mkEnableOption "perles TUI for the beads issue tracker";

  config = lib.mkIf config.phillipgreenii.programs.perles.enable (
    let
      perles = pkgs.callPackage ./package.nix { };
    in
    {
      home.packages = [ perles ];

      # bash completion, inner-gated on the bash shell flag (precedent:
      # home/programs/pre-commit shellAliases inner-gate).
      programs.bash.initExtra = lib.mkIf config.phillipgreenii.programs.bash.enable ''
        source <(perles completion bash 2>/dev/null) || true
      '';
    }
  );
}
