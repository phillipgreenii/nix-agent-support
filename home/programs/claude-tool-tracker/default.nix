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

    ingest = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = cfg.enable;
        defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-tool-tracker.enable";
        example = true;
        description = ''
          Run `claude-tool-tracker ingest` on a timer (a launchd user agent registered by the
          darwin module) so tracker.db keeps up with the event files the hooks append. Without
          it nothing ingests: the hooks only write spool files, and `report --source tracker`
          reads the database, so the report silently stops at the last manual ingest.
        '';
      };

      intervalSeconds = lib.mkOption {
        type = lib.types.int;
        default = 300;
        description = ''
          How often (seconds) to run the ingest. Ingest is incremental (it resumes each file
          from a remembered byte offset) and two runs at once wait for each other, so an
          overlapping tick is harmless.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    # Plugin registration and hooks live in the committed claude-marketplace/ tree;
    # this module only puts the binary on PATH.
    home.packages = [ cfg.package ];
  };
}
