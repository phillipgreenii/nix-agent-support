{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.wsplan;

  # `wsplan` is the read-only land-plan emitter — Stage A of the workforest
  # `land` operation, which the pn-workspace-rules plugin's land-workforest skill
  # runs as a forked read-only subagent before the mutating Stage B. Like its
  # sibling `pnwf` it is invoked as a BARE PATH COMMAND, so without this module it
  # is reachable only via `nix run .#wsplan` and the skill hits
  # command-not-found (bead pg2-a3zez; the same "CLI not on PATH after apply" gap
  # pnwf closed for itself in pg2-xs5cj and integrate-branch-support in pg2-sikj3).
  #
  # Both commands are built by the SAME repo-base module (modules/pnwf) and serve
  # the SAME plugin work-cycle, so the enable trigger is deliberately not a second
  # copy of pnwf's marketplace-resolution expression — it READS pnwf's resolved
  # FEATURE flag. Consequences, all intended:
  #   * the plugin condition (claude-code enabled AND the pn-workspace-rules plugin
  #     enabled) is resolved in exactly ONE place, so the two helpers cannot drift
  #     apart as that resolution changes;
  #   * a machine that vetoes `pnwf.enable` vetoes wsplan too — correct, because
  #     wsplan is Stage A of the very work-cycle pnwf implements (and pnwf is in
  #     wsplan's own runtimeDeps closure);
  #   * an explicit `wsplan.enable = true` still wins upward for a machine that
  #     wants only the emitter.
  # Reading a `programs.*` FEATURE flag is the sanctioned direction (the light
  # capability model forbids gating on `capabilities.*`/`bundles.*`, not on
  # features). Like pnwf and integrate-branch-support, wsplan therefore needs NO
  # capability leaf and no per-machine enable.
  #
  # The `pkgs ? wsplan` term is the availability guard, and it is NOT redundant
  # with pnwf's own: `wsplan` is BUILT IN phillipg-nix-repo-base and threaded into
  # `pkgs.wsplan` by this flake's overlay, which publishes it only on the systems
  # repo-base builds AND only from a repo-base rev that carries it. `wsplan` landed
  # AFTER `pnwf`, so "pnwf present, wsplan absent" is a live state of this flake's
  # own lock, not a hypothetical.
  pnwfEnable = config.phillipgreenii.programs.pnwf.enable;
in
{
  options.phillipgreenii.programs.wsplan = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = pnwfEnable && (pkgs ? wsplan);
      defaultText = lib.literalExpression "config.phillipgreenii.programs.pnwf.enable && (pkgs ? wsplan)";
      example = true;
      description = ''
        Install the `wsplan` CLI (the read-only land-plan emitter the
        pn-workspace-rules plugin's land-workforest skill invokes as a bare PATH
        command for Stage A of `land`). Defaults to whatever its sibling `pnwf`
        resolved to — i.e. on exactly when the pn-workspace-rules plugin itself is
        enabled — so the two helpers of one work-cycle ship together with no
        separate per-machine enable, and the plugin-enabled resolution lives in one
        place rather than being duplicated here. Set false to opt out even when
        `pnwf` is enabled; set true to install it where `pnwf` is not.

        The extra `pkgs ? wsplan` term is the availability guard: `wsplan` is built
        in phillipg-nix-repo-base and threaded in via this flake's system-guarded
        overlay, so it is absent on the systems repo-base does not publish, and
        absent when this flake's locked repo-base rev predates
        `modules/pnwf/wsplan`. Gating on availability makes those cases a graceful
        no-op rather than a hard `wsplan cannot be found in pkgs` eval error at
        apply time. It is a strictly narrower condition than pnwf's own guard,
        because `wsplan` landed in repo-base after `pnwf` did.
      '';
    };
    package = lib.mkPackageOption pkgs "wsplan" { };
  };

  config = lib.mkIf cfg.enable {
    # Shell completions ride along INSIDE the package (mkBashScript installs
    # share/bash-completion/completions/wsplan + share/zsh/site-functions/_wsplan),
    # so home.packages alone puts them on the completion search path — no extra
    # wiring, exactly as for pnwf. The tldr page is the one artifact that needs an
    # explicit hand-off, because tldr resolves custom pages from its own config.
    home.packages = [ cfg.package ];

    programs.tldr.customPages.wsplan = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/wsplan.md";
    };
  };
}
