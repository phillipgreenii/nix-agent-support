{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-activity;
  pkg = cfg.package;
in
{
  options.phillipgreenii.programs.claude-activity = {
    enable = lib.mkEnableOption "claude-activity tracking";
    package = lib.mkPackageOption pkgs "claude-activity" { };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude-code.enable && cfg.enable) {
    home = {
      # Plugin registration + content (plugin.json, hooks/hooks.json) now live in
      # the committed claude-marketplace/ tree, built by the nix marketplace
      # package. This module only installs the binaries on PATH; the marketplace's
      # bare `claude-work-start`/`claude-work-end` hook commands resolve to them.
      packages = [ pkg ];
    };

    programs.tldr.customPages.claude-activity-api = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${pkg}/share/tldr/pages.common/claude-activity-api.md";
    };
  };
}
