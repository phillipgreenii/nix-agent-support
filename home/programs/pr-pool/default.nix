{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pr-pool;
in
{
  options.phillipgreenii.programs.pr-pool = {
    enable = lib.mkEnableOption ''
      pr-pool (PR-feedback orchestrator: a `pr-pool drain` pass discovers ready
      beads and dispatches feedback-processor / worker sessions via ccpool).
      Runtime-depends on `ccpool` and `bd` being on PATH.
    '';
    package = lib.mkPackageOption pkgs "pr-pool" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
