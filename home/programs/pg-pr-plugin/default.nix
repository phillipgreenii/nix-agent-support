{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-pr-plugin;
  contentPkg = cfg.contentPackage;
  pluginVersion = config.phillipgreenii.programs.claude.plugins.local.version;
  marketplaceRoot = ".local/share/pgii-local-plugins";
  contentDir = "${contentPkg}/share/pg-pr-plugin";
in
{
  options.phillipgreenii.programs.pg-pr-plugin = {
    enable = lib.mkEnableOption "pg-pr Claude plugin content (skills, commands, agents, hooks)";
    contentPackage = lib.mkPackageOption pkgs "pg-pr-plugin" { };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    phillipgreenii.programs.claude.plugins.local.plugins.pg-pr = {
      description = "PR-work skills, commands, and agents that drive the pg-pr CLI";
      source = "pg-pr";
      enabledByDefault = true;
    };

    home.file = {
      "${marketplaceRoot}/pg-pr/.claude-plugin/plugin.json" = {
        text = builtins.toJSON {
          name = "pg-pr";
          description = "PR-work skills, commands, and agents that drive the pg-pr CLI";
          version = pluginVersion;
        };
      };

      "${marketplaceRoot}/pg-pr/skills" = {
        source = "${contentDir}/skills";
        recursive = true;
      };

      "${marketplaceRoot}/pg-pr/commands" = {
        source = "${contentDir}/commands";
        recursive = true;
      };

      "${marketplaceRoot}/pg-pr/agents" = {
        source = "${contentDir}/agents";
        recursive = true;
      };

      "${marketplaceRoot}/pg-pr/hooks" = {
        source = "${contentDir}/hooks";
        recursive = true;
      };
    };
  };
}
