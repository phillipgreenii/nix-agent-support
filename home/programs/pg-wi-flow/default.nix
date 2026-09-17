{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-wi-flow;
  cetaCfg = config.phillipgreenii.programs.claude-extended-tool-approver;
in
{
  options.phillipgreenii.programs.pg-wi-flow = {
    enable = lib.mkEnableOption "pg-wi-flow (bead-workflow CLI framework)";
    package = lib.mkPackageOption pkgs "pg-wi-flow" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      pg-wi-flow-identity = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/pg-wi-flow-identity.md";
      };
    };

    # Wire the identity processor into ceta's ordered inputProcessors list
    # (bead tc-q25wo item 3), but only when ceta is itself enabled -- a
    # pg-wi-flow-only machine has nowhere for the processor to run. `mkAfter`
    # (rather than a plain list literal) is deliberate: `inputProcessors` is
    # a `listOf str` that any module MAY contribute to (see rtk, which
    # installs its own binary the same way -- home/programs/rtk/default.nix
    # -- and would append its own entry here at normal priority if/when it
    # grows an input processor of its own), and the bead's own ruling is
    # that this entry runs AFTER rtk's if rtk contributes one. `mkAfter`
    # states that ordering generically (sort after every normal-priority
    # contribution) rather than hardcoding a check for rtk specifically, so
    # it stays correct regardless of what else eventually contributes to
    # this list.
    phillipgreenii.programs.claude-extended-tool-approver.inputProcessors = lib.mkIf cetaCfg.enable (
      lib.mkAfter [ "pg-wi-flow-identity" ]
    );
  };
}
