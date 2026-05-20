{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-pr;
in
{
  options.phillipgreenii.programs.pg-pr = {
    enable = lib.mkEnableOption "pg-pr CLI (unified PR-work CLI)";
    package = lib.mkPackageOption pkgs "pg-pr" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages.pg-pr = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-pr.md";
    };

    # Phase 0: install the CLI only. Config-file generation, daemon unit,
    # plugin marketplace registration land in subsequent phases.
  };
}
