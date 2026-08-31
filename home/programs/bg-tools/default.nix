{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.bg-tools;
in
{
  options.phillipgreenii.programs.bg-tools = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the bgrun/bgcheck background-job helpers. These serve Claude agent sessions
        (launching long-running commands detached and checking on them later without an
        inherited process boundary), so they default on exactly when Claude is enabled.
      '';
    };
    package = lib.mkPackageOption pkgs "bg-tools" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      bgrun = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/bgrun.md";
      };
      bgcheck = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/bgcheck.md";
      };
    };
  };
}
