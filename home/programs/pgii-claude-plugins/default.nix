{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  th = cfg.plugins.thirdparty;

  allMarketplacePlugins = lib.concatLists (
    lib.mapAttrsToList (
      mktName: mkt:
      map (p: {
        name = p;
        marketplace = mktName;
      }) mkt.plugins
    ) th.marketplaces
  );

  allOfficialPlugins = map (p: {
    name = p;
    marketplace = "claude-plugins-official";
  }) th.officialPlugins;

  allPlugins = allMarketplacePlugins ++ allOfficialPlugins;

  computedEnabled = lib.listToAttrs (
    map (
      p:
      let
        key = "${p.name}@${p.marketplace}";
      in
      lib.nameValuePair key (th.overrides.${key} or (th.enabledByDefault.${p.name} or true))
    ) allPlugins
  );
in
{
  options.phillipgreenii.programs.claude.plugins.thirdparty = {
    marketplaces = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options = {
            repo = lib.mkOption {
              type = lib.types.str;
              description = "GitHub owner/repo for the marketplace";
            };
            plugins = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              default = [ ];
            };
          };
        }
      );
      default = { };
    };

    officialPlugins = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
    };

    lspPackages = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
    };

    enabledByDefault = lib.mkOption {
      type = lib.types.attrsOf lib.types.bool;
      default = { };
      description = "Per-plugin default enabled state. Key is plugin name (no @marketplace). Absent = true.";
    };

    overrides = lib.mkOption {
      type = lib.types.attrsOf lib.types.bool;
      default = { };
      example = lib.literalExpression ''{ "some-plugin@my-mkt" = false; }'';
      description = "Per-plugin override. Key is 'name@marketplace'. Overrides enabledByDefault.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = th.lspPackages;

    phillipgreenii.programs.claude.settings = {
      extraKnownMarketplaces = lib.mapAttrs (_: mkt: {
        source = {
          source = "github";
          inherit (mkt) repo;
        };
      }) th.marketplaces;
      enabledPlugins = computedEnabled;
      plugins = map (p: "${p.name}@${p.marketplace}") allPlugins;
    };
  };
}
