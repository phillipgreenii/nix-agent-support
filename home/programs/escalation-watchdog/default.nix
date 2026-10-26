{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.escalation-watchdog;
in
{
  options.phillipgreenii.programs.escalation-watchdog = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      example = true;
      description = ''
        Install the escalation-watchdog CLI: one tick hands beads that stayed `escalated` longer
        than `--stale-after` to the operator with `hand-to-human --also-add stale-escalation
        --note ...` (docs/guides/bead-label-vocabulary.md and ADR 0105). This module only installs
        the command; the timer or router role that runs it, and the durations, belong to the
        deployment flake. The command needs `bd` and `hand-to-human` on the PATH it runs with.
      '';
    };
    package = lib.mkPackageOption pkgs "escalation-watchdog" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      escalation-watchdog = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/escalation-watchdog.md";
      };
    };
  };
}
