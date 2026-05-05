{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.my-code-review-support;
  cliPkg = cfg.cliPackage;
  contentPkg = cfg.contentPackage;
  pluginVersion = config.phillipgreenii.programs.claude.plugins.local.version;
  marketplaceRoot = ".local/share/pgii-local-plugins";
  contentDir = "${contentPkg}/share/my-code-review-support";
in
{
  options.phillipgreenii.programs.my-code-review-support = {
    enable = lib.mkEnableOption "my-code-review-support plugin";
    cliPackage = lib.mkPackageOption pkgs "my-code-review-support-cli" { };
    contentPackage = lib.mkPackageOption pkgs "my-code-review-support" { };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    phillipgreenii.programs.claude.plugins.local.plugins.my-code-review-support = {
      description = "AI-powered code review support for GitHub PRs";
      source = "my-code-review-support";
      enabledByDefault = true;
    };

    home = {
      packages = [ cliPkg ];

      file = {
        "${marketplaceRoot}/my-code-review-support/.claude-plugin/plugin.json" = {
          text = builtins.toJSON {
            name = "my-code-review-support";
            description = "AI-powered code review support for GitHub PRs";
            version = pluginVersion;
          };
        };

        "${marketplaceRoot}/my-code-review-support/agents" = {
          source = "${contentDir}/agents";
          recursive = true;
        };

        "${marketplaceRoot}/my-code-review-support/skills" = {
          source = "${contentDir}/skills";
          recursive = true;
        };

        "${marketplaceRoot}/my-code-review-support/references" = {
          source = "${contentDir}/references";
          recursive = true;
        };
      };
    };

    programs.tldr.customPages.my-code-review-support-cli = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cliPkg}/share/tldr/pages.common/my-code-review-support-cli.md";
    };
  };
}
