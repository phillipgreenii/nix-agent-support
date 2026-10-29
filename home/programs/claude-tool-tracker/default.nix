{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude-tool-tracker;
in
{
  options.phillipgreenii.programs.claude-tool-tracker = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the claude-tool-tracker binary that the claude-tool-tracker plugin's command
        hooks invoke (a bare `claude-tool-tracker hook`). Defaults on exactly when Claude is
        enabled, since the plugin is default-enabled and its hooks fire on every consumer.
      '';
    };
    package = lib.mkPackageOption pkgs "claude-tool-tracker" { };
  };

  config = lib.mkIf cfg.enable {
    # Plugin registration and hooks live in the committed claude-marketplace/ tree;
    # this module only puts the binary on PATH.
    home.packages = [ cfg.package ];
  };
}
