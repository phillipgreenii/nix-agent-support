{
  config,
  lib,
  pkgs,
  mkBashBuildersFor,
  ...
}:
with lib;
let
  cfg = config.phillipgreenii.programs.pg2-agent;
  bashBuilders = mkBashBuildersFor pkgs;
  sortedAgents = lib.sort (a: b: a.priority < b.priority) (lib.attrValues cfg.agents);
  agentsList = map (agent: {
    inherit (agent) id priority;
    script = toString agent.script;
  }) sortedAgents;
  pg2AgentScripts = import ./scripts.nix {
    inherit pkgs bashBuilders;
    agents = agentsList;
  };
in
{
  options.phillipgreenii.programs.pg2-agent = {
    enable = mkEnableOption "pg2-agent priority-based AI agent dispatcher";

    agents = mkOption {
      type = types.attrsOf (
        types.submodule {
          options = {
            id = mkOption {
              type = types.str;
              description = "Unique identifier for the agent";
            };
            priority = mkOption {
              type = types.int;
              description = "Execution priority (lower = tried first)";
            };
            script = mkOption {
              type = types.path;
              description = ''
                Path to agent script accepting positional args: <model> <plan> <thinking> <prompt>
                Exit codes: 0=success 1=error 11=auth-error 12=license-error
              '';
            };
          };
        }
      );
      default = { };
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = builtins.length sortedAgents > 0;
        message = "pg2-agent: at least one agent must be registered (enable claude)";
      }
    ];

    home-manager.users.phillipg = {
      home.packages = pg2AgentScripts.packages;
      programs.tldr.customPages = lib.mapAttrs (_: content: {
        platform = "common";
        inherit content;
      }) pg2AgentScripts.tldr;
    };
  };
}
