{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.bash-scripting;
  contentPkg = cfg.contentPackage;
  marketplaceRoot = ".local/share/pgii-local-plugins";
  contentDir = "${contentPkg}/share/bash-scripting";
in
{
  options.phillipgreenii.programs.bash-scripting = {
    enable = lib.mkEnableOption "bash-scripting Claude skill plugin";
    contentPackage = lib.mkPackageOption pkgs "bash-scripting" { };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    phillipgreenii.programs.claude.plugins.local.plugins.bash-scripting = {
      description = "Claude skill: bash scripting conventions for the mkBashBuilders framework";
      source = "bash-scripting";
      enabledByDefault = true;
    };

    home.file = {
      "${marketplaceRoot}/bash-scripting/.claude-plugin/plugin.json" = {
        text = builtins.toJSON {
          name = "bash-scripting";
          description = "Claude skill: bash scripting conventions for the mkBashBuilders framework";
          inherit (config.phillipgreenii.programs.claude.plugins.local) version;
        };
      };

      "${marketplaceRoot}/bash-scripting/skills" = {
        source = "${contentDir}/skills";
        recursive = true;
      };
    };
  };
}
