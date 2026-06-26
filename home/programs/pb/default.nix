{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pb;
in
{
  options.phillipgreenii.programs.pb = {
    enable = lib.mkEnableOption ''
      pb (phillip-beads: writes and resolves pn:applied gates that hold beads
      until a `pn workspace apply` applies their change). `bd` and `git` are
      wired onto PATH via wrapProgram; `pn` MUST be provided on PATH by the
      environment (the apply post-hook env and dev shells already have it).
    '';
    package = lib.mkPackageOption pkgs "pb" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
