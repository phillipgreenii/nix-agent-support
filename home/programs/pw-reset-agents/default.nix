{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pw-reset-agents;
in
{
  options.phillipgreenii.programs.pw-reset-agents.enable =
    lib.mkEnableOption "pw-reset-agents — stop all waiting agents";
  config = lib.mkIf cfg.enable {
    home.packages = [ pkgs.pw-reset-agents ];

    # tldr resolves custom pages from its own config, so the page the package
    # ships needs this explicit hand-off to be reachable (same wiring as
    # wait-for-agents).
    programs.tldr.customPages.pw-reset-agents = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${pkgs.pw-reset-agents}/share/tldr/pages.common/pw-reset-agents.md";
    };
  };
}
