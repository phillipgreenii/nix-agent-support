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
  # It is a git landing detector, NOT a pn tool (bead tc-2k1o8, epic tc-ys2l8), so its
  # own default is `claude-code.enable`, independent of `phillipgreenii.pn.enable`.
  # The plugin follows this option: this module is the ONE owner of the
  # `marketplaces.overrides."integrate-branch@<mkt>"` key and sets it from `cfg.enable`
  # UNCONDITIONALLY (outside any mkIf, so it forces the plugin OFF when the detector is
  # off). It MUST NOT read the overrides or the plugin's resolved enable back: that is
  # the cycle (default -> overrides -> default) the former `pluginEnabled` read risked.
in
{
  options.phillipgreenii.programs.integrate-branch-support = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the integrate-branch-support CLI (the detector the integrate-branch
        plugin's dispatcher skill invokes as a bare PATH command). Defaults on exactly
        when claude-code is enabled, and the integrate-branch plugin is tied to THIS
        option (see the override below), so the detector and the plugin's skills ship
        together with no separate per-machine enable — closing the "CLI not on PATH
        after apply" gap (pg2-sikj3). Set false to opt out; that also turns the
        plugin off.
      '';
    };
    package = lib.mkPackageOption pkgs "integrate-branch-support" { };
  };

  config = lib.mkMerge [
    {
      # Sole owner of this override key; unconditional so it tracks cfg.enable both ways.
      phillipgreenii.programs.claude-code.marketplaces.overrides."integrate-branch@phillipgreenii-nix-agent-support-marketplace-local" =
        cfg.enable;
    }
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      programs.tldr.customPages.integrate-branch-support = lib.mkIf config.programs.tldr.enable {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/integrate-branch-support.md";
      };
    })
  ];
}
