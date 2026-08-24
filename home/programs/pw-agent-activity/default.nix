{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pw-agent-activity;
in
{
  options.phillipgreenii.programs.pw-agent-activity.enable =
    lib.mkEnableOption "pw-agent-activity — wait for all agents to finish";
  config = lib.mkIf cfg.enable {
    home.packages = [ pkgs.pw-agent-activity ];

    # tldr resolves custom pages from its own config, so the page the package
    # ships needs this explicit hand-off to be reachable (same wiring as
    # wait-for-agents).
    programs.tldr.customPages.pw-agent-activity = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${pkgs.pw-agent-activity}/share/tldr/pages.common/pw-agent-activity.md";
    };
  };
}
