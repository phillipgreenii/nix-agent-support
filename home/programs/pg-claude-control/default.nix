{
  config,
  lib,
  pkgs,
  ...
}:

# pg-claude-control module (docket pg2-v77tsx, phase 8c): puts the control
# command `pg-claude-control` on PATH and renders the two settings the control
# mod (claude-marketplace/pg-claude-control) and the command share.
#
# The plugin itself is delivered by the committed claude-marketplace/ tree (it is
# default-enabled); this module installs the binary and the settings. `enable`
# defaults to the claude-code module's flag so the plugin and the command it
# needs are on together (test-claude-plugin-binary-default-parity, "default-
# enabled plugin => default-enabled binary"). The plugin's hooks.json names a
# hooks module and no bare command, so that check's scan has nothing to match
# for this plugin; its pluginProgram row is still present so a later bare hook
# command is held to the same rule.
#
# The shared mechanism (fixed by the mod, read identically by the command):
#   PG_CLAUDE_CONTROL_STATE_DIR  the state directory (inbox/ and ends/ under it)
#   PG_CLAUDE_CONTROL_RUN_ALLOW  comma-separated run.allow command names
# They are delivered as home.sessionVariables. The recorder module's
# controlStateDir defaults from `stateDir` here (and asserts on a mismatch).
let
  cfg = config.phillipgreenii.programs.pg-claude-control;
in
{
  options.phillipgreenii.programs.pg-claude-control = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install the pg-claude-control command and render the settings the control mod and
        the command share. Defaults on exactly when Claude is enabled, since the control
        plugin is default-enabled.
      '';
    };

    package = lib.mkPackageOption pkgs "pg-claude-control" { };

    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.stateHome}/pg-claude-control";
      defaultText = lib.literalExpression ''"''${config.xdg.stateHome}/pg-claude-control"'';
      description = ''
        The control state directory: the per-process inbox is `inbox/` and the session end
        files are `ends/` under it. Exported as PG_CLAUDE_CONTROL_STATE_DIR to the mod and
        the command; the recorder's `controlStateDir` defaults to this value.
      '';
    };

    run.allow = lib.mkOption {
      type = lib.types.listOf (lib.types.strMatching "[A-Za-z0-9_-]+");
      default = [
        "exit"
        "clear"
        "compact"
      ];
      description = ''
        Slash commands a tool may run through the mod (`run`). MUST contain `exit`, because
        `stop` runs it. Exported comma-separated as PG_CLAUDE_CONTROL_RUN_ALLOW.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    home.sessionVariables = {
      PG_CLAUDE_CONTROL_STATE_DIR = cfg.stateDir;
      PG_CLAUDE_CONTROL_RUN_ALLOW = lib.concatStringsSep "," cfg.run.allow;
    };

    assertions = [
      {
        assertion = lib.elem "exit" cfg.run.allow;
        message = "phillipgreenii.programs.pg-claude-control.run.allow must contain \"exit\": `pg-claude-control stop` runs it";
      }
    ];
  };
}
