{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-activity;
  pkg = cfg.package;
  pluginVersion = config.phillipgreenii.programs.claude.plugins.local.version;
  marketplaceRoot = ".local/share/pgii-local-plugins";
in
{
  options.phillipgreenii.programs.claude-activity = {
    enable = lib.mkEnableOption "claude-activity tracking";
    package = lib.mkPackageOption pkgs "claude-activity" { };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    phillipgreenii.programs.claude.plugins.local.plugins.claude-activity = {
      description = "Claude Code activity tracking via hooks";
      source = "claude-activity";
      enabledByDefault = true;
    };

    home = {
      packages = [ pkg ];

      file."${marketplaceRoot}/claude-activity/.claude-plugin/plugin.json" = {
        text = builtins.toJSON {
          name = "claude-activity";
          description = "Claude Code activity tracking via hooks";
          version = pluginVersion;
        };
      };

      file."${marketplaceRoot}/claude-activity/hooks/hooks.json" = {
        text = builtins.toJSON {
          description = "Tracks Claude agent activity sessions";
          hooks = {
            UserPromptSubmit = [
              {
                hooks = [
                  {
                    type = "command";
                    command = "${pkg}/bin/claude-work-start";
                  }
                ];
              }
            ];
            Stop = [
              {
                hooks = [
                  {
                    type = "command";
                    command = "${pkg}/bin/claude-work-end";
                  }
                ];
              }
            ];
          };
        };
      };
    };

    programs.tldr.customPages.claude-activity-api = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${pkg}/share/tldr/pages.common/claude-activity-api.md";
    };
  };
}
