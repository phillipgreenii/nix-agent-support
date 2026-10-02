{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.handoff-create;
in
{
  options.phillipgreenii.programs.handoff-create = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the handoff-create CLI: creates a handoff bead correctly (type, P0, title
        prefix, first body line, session metadata, the human-label policy, the task fallback, and
        a read-back), for session-wrapup:wrap-up-session and the beads-lifecycle:handoff-bead
        skill. Defaults on exactly when Claude is enabled, since it exists only to serve a Claude
        Code session.
      '';
    };
    package = lib.mkPackageOption pkgs "handoff-create" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      handoff-create = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/handoff-create.md";
      };
    };
  };
}
