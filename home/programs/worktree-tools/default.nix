{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.worktree-tools;
in
{
  options.phillipgreenii.programs.worktree-tools = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Single toggle for the worktree CLIs (wtnew, wtdone, wtaudit). Defaults on exactly
        when claude-code is enabled. When on it enables each tool at `lib.mkDefault true`
        (never `mkDefault false`), so an explicit `programs.<tool>.enable` setting wins.

        Plugins do not require this toggle; each requires the exact tool it calls
        (`wtdone` for pb, integrate-branch and session-wrapup; `wtdone` and `wtaudit` for
        pn-workspace-rules), so a plugin is never on while its tool is off. Set false to
        opt out; that turns off the tools and therefore those plugins.
      '';
    };
  };

  # This module MUST NOT read the plugin overrides or a plugin's resolved enable, MUST NOT
  # set marketplaces.overrides (the cycle default -> overrides -> default), and MUST NOT
  # reference the capability or bundle option trees (features never know their groupings).
  config = lib.mkIf cfg.enable {
    phillipgreenii.programs = {
      wtnew.enable = lib.mkDefault true;
      wtdone.enable = lib.mkDefault true;
      wtaudit.enable = lib.mkDefault true;
    };
  };
}
