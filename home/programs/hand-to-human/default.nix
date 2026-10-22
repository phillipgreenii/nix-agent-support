{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.hand-to-human;
in
{
  options.phillipgreenii.programs.hand-to-human = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the hand-to-human CLI: hands a bead to the operator by writing the strict label
        pair human + attention and removing escalated in ONE update call (the bead label
        vocabulary, docs/guides/bead-label-vocabulary.md and ADR 0105). Used by the
        beads-lifecycle skill's LV rules and by the escalation-triager sessions. Defaults on
        exactly when Claude is enabled, since it exists only to serve agent sessions.
      '';
    };
    package = lib.mkPackageOption pkgs "hand-to-human" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      hand-to-human = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/hand-to-human.md";
      };
    };
  };
}
