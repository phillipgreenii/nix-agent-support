{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pnwf;

  # `pnwf` is the deterministic helper the pn-workspace-rules plugin's workforest
  # stage-skills (fork/validate/land/cleanup-workforest) and the /pn-workspace-sync
  # command invoke as a bare PATH command. It MUST ship and enable together with that
  # plugin, otherwise the skills hit command-not-found and the whole workforest
  # work-cycle is dead (the "CLI not on PATH after apply" gap, pg2-sikj3 / pg2-xs5cj).
  #
  # Direction of the tie (bead tc-2k1o8, epic tc-ys2l8 decisions D3/D4): pnwf is a
  # tool of the `pn` workspace tool, so its enable DEFAULT follows the single
  # machine-chosen boolean `phillipgreenii.pn.enable` (declared by nix-repo-base's
  # home/pn module, RB-1), NOT the plugin's resolved state. The plugin follows pnwf:
  # this module is the ONE owner of the
  # `marketplaces.overrides."pn-workspace-rules@<mkt>"` key and sets it from
  # `cfg.enable` UNCONDITIONALLY (outside any mkIf, so it forces the plugin OFF when
  # pnwf is off too; an mkIf-guarded assignment could only ever turn it on). It MUST
  # NOT read the overrides or the plugin's resolved enable back: that is the cycle
  # (default -> overrides -> default) the former `pluginEnabled` read risked. No other
  # module may set this override key.
  #
  # Unlike integrate-branch-support (whose binary is built in THIS repo's overlay), the
  # `pnwf` package is BUILT IN phillipg-nix-repo-base (modules/pnwf) and threaded into
  # `pkgs.pnwf` by this flake's overlay (system-guarded: repo-base publishes only
  # x86_64-linux + aarch64-darwin). `mkPackageOption pkgs "pnwf"` therefore resolves the
  # repo-base-built package present in pkgs on the published systems.
in
{
  options.phillipgreenii.programs.pnwf = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.pn.enable && (pkgs ? pnwf);
      defaultText = lib.literalExpression "config.phillipgreenii.pn.enable && (pkgs ? pnwf)";
      example = true;
      description = ''
        Install the `pnwf` CLI (the deterministic helper the pn-workspace-rules
        plugin's workforest stage-skills and /pn-workspace-sync command invoke as a
        bare PATH command). Defaults on exactly when `phillipgreenii.pn.enable` is on,
        and the pn-workspace-rules plugin is tied to THIS option (see the override
        below), so the helper and the plugin's skills ship together under the one
        machine-chosen pn boolean (tc-2k1o8). Set false to opt out even when pn is on;
        that also turns the plugin off.

        The extra `pkgs ? pnwf` term is defense-in-depth: unlike
        integrate-branch-support (built in this repo's own overlay), `pnwf` is built
        in phillipg-nix-repo-base and threaded in via the system-guarded overlay. It is
        absent on the systems repo-base does not publish, AND absent when this flake's
        locked repo-base rev predates `modules/pnwf`. Gating on availability makes those
        cases a graceful no-op rather than a hard `pnwf cannot be found in pkgs` eval
        error at apply time.
      '';
    };
    package = lib.mkPackageOption pkgs "pnwf" { };
  };

  config = lib.mkMerge [
    {
      # Sole owner of this override key; unconditional so it tracks cfg.enable both ways.
      phillipgreenii.programs.claude-code.marketplaces.overrides."pn-workspace-rules@phillipg-nix-repo-base-marketplace-local" =
        cfg.enable;
    }
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      programs.tldr.customPages.pnwf = lib.mkIf config.programs.tldr.enable {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/pnwf.md";
      };
    })
  ];
}
