{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-extended-tool-approver;
  pkg = cfg.package;
  hookPkg =
    if cfg.inputProcessor != null then
      pkgs.symlinkJoin {
        name = "${pkg.name}-wrapped";
        paths = [ pkg ];
        nativeBuildInputs = [ pkgs.makeWrapper ];
        postBuild = ''
          wrapProgram $out/bin/claude-extended-tool-approver \
            --set CETA_INPUT_PROCESSOR "${cfg.inputProcessor}"
        '';
      }
    else
      pkg;
  pluginVersion = config.phillipgreenii.programs.claude.plugins.local.version;
  marketplaceRoot = ".local/share/pgii-local-plugins";
in
{
  options.phillipgreenii.programs.claude-extended-tool-approver = {
    enable = lib.mkEnableOption "claude-extended-tool-approver permission evaluator";
    package = lib.mkPackageOption pkgs "claude-extended-tool-approver" { };
    inputProcessor = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Command to rewrite bash commands before execution.
        Called as: <command> "<bash-command>".
        Exit 0 + stdout = rewritten command, exit 1+ = no rewrite.
      '';
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    phillipgreenii.programs.claude.plugins.local.plugins.claude-extended-tool-approver = {
      description = "Pre-tool permission hook with rule-based evaluation";
      source = "claude-extended-tool-approver";
      enabledByDefault = true;
    };

    home = {
      packages = [ pkg ];

      file = {
        "${marketplaceRoot}/claude-extended-tool-approver/.claude-plugin/plugin.json" = {
          text = builtins.toJSON {
            name = "claude-extended-tool-approver";
            description = "Pre-tool permission hook with rule-based evaluation";
            version = pluginVersion;
          };
        };

        "${marketplaceRoot}/claude-extended-tool-approver/skills" = {
          source = "${pkg}/share/claude-extended-tool-approver/skills";
          recursive = true;
        };

        "${marketplaceRoot}/claude-extended-tool-approver/hooks/hooks.json" = {
          text = builtins.toJSON {
            description = "Pre-tool permission hook with rule-based evaluation and decision logging";
            hooks = {
              PreToolUse = [
                {
                  hooks = [
                    {
                      type = "command";
                      command = "${hookPkg}/bin/claude-extended-tool-approver";
                      timeout = 5;
                    }
                  ];
                }
              ];
              PermissionRequest = [
                {
                  hooks = [
                    {
                      type = "command";
                      command = "${hookPkg}/bin/claude-extended-tool-approver";
                      timeout = 5;
                    }
                  ];
                }
              ];
              PostToolUse = [
                {
                  hooks = [
                    {
                      type = "command";
                      command = "${hookPkg}/bin/claude-extended-tool-approver";
                      timeout = 5;
                    }
                  ];
                }
              ];
              PermissionDenied = [
                {
                  hooks = [
                    {
                      type = "command";
                      command = "${hookPkg}/bin/claude-extended-tool-approver";
                      timeout = 5;
                    }
                  ];
                }
              ];
              SessionEnd = [
                {
                  hooks = [
                    {
                      type = "command";
                      command = "${hookPkg}/bin/claude-extended-tool-approver";
                      timeout = 5;
                    }
                  ];
                }
              ];
            };
          };
        };
      };
    };
  };
}
