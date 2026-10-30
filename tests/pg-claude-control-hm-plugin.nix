# Eval-level test of home/programs/pg-claude-control (docket pg2-v77tsx, phase 8c), in the
# style of tests/pg-claude-code-recorder-hm-launchd.nix: the module is evaluated against
# stubbed home-manager options (assertions, warnings, home.homeDirectory, home.file,
# xdg.configFile) and a stub of the claude-code module's `enable` flag. Consumed by
# checks.<system>.test-pg-claude-control-hm-plugin.
#
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  evaluate =
    {
      claudeCode ? true,
      cfg ? { },
    }:
    lib.evalModules {
      specialArgs = {
        inherit pkgs lib;
      };
      modules = [
        ../home/programs/pg-claude-control/default.nix
        (
          { lib, ... }:
          {
            options = {
              phillipgreenii.programs.claude-code.enable = lib.mkOption {
                type = lib.types.bool;
                default = false;
              };
              xdg = {
                configFile = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
                stateHome = lib.mkOption {
                  type = lib.types.str;
                  default = "/example/home/.local/state";
                };
              };
              home = {
                homeDirectory = lib.mkOption {
                  type = lib.types.str;
                  default = "/example/home";
                };
                sessionVariables = lib.mkOption {
                  type = lib.types.attrsOf lib.types.str;
                  default = { };
                };
                file = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
                packages = lib.mkOption {
                  type = lib.types.listOf lib.types.package;
                  default = [ ];
                };
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
              warnings = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
              };
            };
          }
        )
        {
          phillipgreenii.programs.claude-code.enable = claudeCode;
          phillipgreenii.programs.pg-claude-control = cfg;
        }
      ];
    };

  opts = c: c.config.phillipgreenii.programs.pg-claude-control;
  vars = c: c.config.home.sessionVariables;
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);

  withClaude = evaluate { };
  withoutClaude = evaluate { claudeCode = false; };
  explicitOn = evaluate {
    claudeCode = false;
    cfg.enable = true;
  };
  explicitOff = evaluate { cfg.enable = false; };
  custom = evaluate {
    cfg = {
      stateDir = "/example/control";
      run.allow = [
        "exit"
        "clear"
      ];
    };
  };
  noExit = evaluate {
    cfg.run.allow = [
      "clear"
      "compact"
    ];
  };

  cases = {
    # ---- enable follows the claude-code flag ---------------------------------
    enableFollowsClaudeCode = (opts withClaude).enable == true && (opts withoutClaude).enable == false;
    commandOnWithClaudeCode =
      map toString withClaude.config.home.packages == [ (toString pkgs.pg-claude-control) ];
    nothingWithoutClaudeCode = withoutClaude.config.home.packages == [ ] && vars withoutClaude == { };
    explicitEnableWins = (opts explicitOn).enable == true && explicitOn.config.home.packages != [ ];
    explicitDisableWins = explicitOff.config.home.packages == [ ] && vars explicitOff == { };

    # ---- defaults, as rendered -----------------------------------------------
    stateDirDefault = (opts withClaude).stateDir == "/example/home/.local/state/pg-claude-control";
    runAllowDefault =
      (opts withClaude).run.allow == [
        "exit"
        "clear"
        "compact"
      ];
    renderedDefaults =
      vars withClaude == {
        PG_CLAUDE_CONTROL_STATE_DIR = "/example/home/.local/state/pg-claude-control";
        PG_CLAUDE_CONTROL_RUN_ALLOW = "exit,clear,compact";
      };
    defaultsAssertNothing = failedAssertions withClaude == [ ] && withClaude.config.warnings == [ ];

    # ---- a user-set value reaches the shared mechanism ------------------------
    customStateDirRendered = (vars custom).PG_CLAUDE_CONTROL_STATE_DIR == "/example/control";
    customRunAllowRendered = (vars custom).PG_CLAUDE_CONTROL_RUN_ALLOW == "exit,clear";
    customNoAssertion = failedAssertions custom == [ ];

    # ---- run.allow must contain exit ------------------------------------------
    noExitAsserts =
      builtins.length (messages noExit) == 1
      && lib.hasInfix "run.allow" (builtins.head (messages noExit))
      && lib.hasInfix "exit" (builtins.head (messages noExit));

    # ---- nothing else is written ----------------------------------------------
    writesNoFiles = withClaude.config.home.file == { } && withClaude.config.xdg.configFile == { };
  };
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  live = {
    package = pkgs.pg-claude-control;
    stateDir = (vars custom).PG_CLAUDE_CONTROL_STATE_DIR;
    runAllow = (vars custom).PG_CLAUDE_CONTROL_RUN_ALLOW;
  };
}
