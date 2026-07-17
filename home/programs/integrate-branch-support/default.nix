{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.integrate-branch-support;

  # The integrate-branch-support CLI is the detector the integrate-branch plugin's
  # dispatcher skill invokes as a bare PATH command. It MUST ship and enable together
  # with that plugin, otherwise the dispatcher hits command-not-found and the whole
  # landing flow (Tier R R-9) is dead — the "CLI not on PATH after apply" incident
  # pg2-sikj3 fixes.
  #
  # So default `enable` to whether the integrate-branch PLUGIN is enabled, mirroring
  # claude-marketplaces' own resolution (override-by-key -> override-by-name -> plugin
  # defaultEnabled) over the active nix-provided marketplaces. Leaf options are read
  # directly (never the whole `claude` attrset) to avoid the freeform/unmatchedDefns
  # recursion that module documents. The `claudeEnable &&` guard matters: `nixProvided`
  # carries plugin metadata even on hosts where Claude is disabled, so without it the
  # CLI could install where the plugin never registers.
  claudeEnable = config.phillipgreenii.programs.claude-code.enable;
  mcfg = config.phillipgreenii.programs.claude-code.marketplaces;
  activeMarketplaces = lib.filter (m: mcfg.enabled.${m.marketplaceName} or true) mcfg.nixProvided;
  pluginEnabled =
    name:
    lib.any (
      m:
      lib.any (
        p: p.name == name && (mcfg.overrides.${p.key} or mcfg.overrides.${p.name} or p.defaultEnabled)
      ) m.plugins
    ) activeMarketplaces;
in
{
  options.phillipgreenii.programs.integrate-branch-support = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = claudeEnable && pluginEnabled "integrate-branch";
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable && <integrate-branch plugin enabled>";
      example = true;
      description = ''
        Install the integrate-branch-support CLI (the detector the integrate-branch
        plugin's dispatcher skill invokes as a bare PATH command). Defaults on exactly
        when the integrate-branch plugin itself is enabled, so the detector and the
        plugin's skills ship together with no separate per-machine enable — closing the
        "CLI not on PATH after apply" gap (pg2-sikj3). Set false to opt out even when
        the plugin is enabled.
      '';
    };
    package = lib.mkPackageOption pkgs "integrate-branch-support" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages.integrate-branch-support = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/integrate-branch-support.md";
    };
  };
}
