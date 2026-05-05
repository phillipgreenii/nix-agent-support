{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  marketplaceName = "pgii-local-plugins";
  marketplaceRoot = ".local/share/${marketplaceName}";

  computedEnabled = lib.mapAttrs' (
    name: _:
    lib.nameValuePair "${name}@${marketplaceName}" (
      cfg.plugins.local.overrides.${name} or cfg.plugins.local.plugins.${name}.enabledByDefault
    )
  ) cfg.plugins.local.plugins;
in
{
  options.phillipgreenii.programs.claude.plugins.local = {
    plugins = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options = {
            description = lib.mkOption {
              type = lib.types.str;
              description = "Plugin description shown in marketplace UI";
            };
            source = lib.mkOption {
              type = lib.types.str;
              example = "agent-rules";
              description = "Relative path from marketplace root to plugin directory";
            };
            enabledByDefault = lib.mkOption {
              type = lib.types.bool;
              default = true;
              description = "Whether enabled by default. Override per-machine via plugins.local.overrides.";
            };
          };
        }
      );
      default = { };
      description = "Local plugins registered in pgii-local-plugins";
    };

    version = lib.mkOption {
      type = lib.types.str;
      default = "0.0.0";
      description = "Marketplace and plugin version (set from flake lib.pluginVersion)";
    };

    overrides = lib.mkOption {
      type = lib.types.attrsOf lib.types.bool;
      default = { };
      example = lib.literalExpression "{ bash-lsp = false; }";
      description = "Per-plugin enable override. Key is plugin name (no @marketplace). Overrides enabledByDefault.";
    };
  };

  config = lib.mkIf (cfg.enable && cfg.plugins.local.plugins != { }) {
    home.file."${marketplaceRoot}/.claude-plugin/marketplace.json".text = builtins.toJSON {
      name = marketplaceName;
      owner.name = "phillipgreenii";
      plugins = lib.mapAttrsToList (name: plugin: {
        inherit name;
        inherit (plugin) description source;
        inherit (cfg.plugins.local) version;
      }) cfg.plugins.local.plugins;
    };

    phillipgreenii.programs.claude.settings = {
      extraKnownMarketplaces.${marketplaceName} = {
        source = {
          source = "directory";
          path = "${config.home.homeDirectory}/${marketplaceRoot}";
        };
      };
      enabledPlugins = computedEnabled;
      plugins = lib.mapAttrsToList (name: _: "${name}@${marketplaceName}") cfg.plugins.local.plugins;
    };
  };
}
