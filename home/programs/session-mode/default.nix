{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.session-mode;
in
{
  options.phillipgreenii.programs.session-mode = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the session-mode CLI: per-session "which loop is running" tracking
        (drain-beads / unblock-human-beads / wrap-up-session / ...), consumed by the pb
        marketplace's commands, its SessionEnd hook, and the claude-status-line segment.
        Defaults on exactly when Claude is enabled, since it exists only to serve a Claude
        Code session.
      '';
    };
    package = lib.mkPackageOption pkgs "session-mode" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      session-mode = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/session-mode.md";
      };
    };
  };
}
