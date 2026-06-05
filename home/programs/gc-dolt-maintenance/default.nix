{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.gc-dolt-maintenance;
in
{
  options.phillipgreenii.programs.gc-dolt-maintenance = {
    enable = lib.mkEnableOption "gc dolt maintenance tools";
    cityPath = lib.mkOption {
      type = lib.types.str;
      default = "/Users/phillipg/gc";
      description = "Path to the gc city";
    };
  };

  # Grafana dashboard registration and LaunchAgent wiring live in the parallel
  # darwin module (darwin/modules/gc-dolt-maintenance) because the relevant
  # options are declared at darwin/system scope, not HM scope. Per ADR 0049
  # the LaunchAgent MUST go through phillipgreenii.system.launchdServices —
  # writing launchd.agents directly from this HM module is forbidden.
  config = lib.mkIf cfg.enable {
    home.packages = [
      pkgs.gc-bd-import-breaker
      pkgs.gc-dolt-maintenance
    ];

    home.activation.gcBdImportBreaker = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      run ${pkgs.gc-bd-import-breaker}/bin/gc-bd-import-breaker apply --city ${lib.escapeShellArg cfg.cityPath} || true
    '';
  };
}
