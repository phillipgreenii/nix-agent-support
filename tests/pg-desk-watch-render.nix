# Eval-level fixture for home/programs/pg-desk's derived watch.issue keys
# (docket pg2-2qyyh.3, packet P3-06). Evaluates the pg-desk and pg-connector
# modules together (stubbing only home.packages and xdg.configFile, like
# tests/pg-desk-attention-registration.nix) and returns, per scenario, the
# rendered pg-desk config.yaml and the evaluated assertions. Consumed by
# checks.test-pg-desk-watch-render, which parses each YAML file.
#
# All names are EXAMPLE values for fixtures only; a deployment's real queries
# live in its own machine flake.
{ lib, pkgs }:
let
  evaluate =
    {
      connector ? { },
      desk ? { },
    }:
    (lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../home/programs/pg-desk/default.nix
        ../home/programs/pg-connector/default.nix
        (
          { lib, ... }:
          {
            options = {
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              programs.tldr.enable = lib.mkEnableOption "tldr (stub)";
              programs.tldr.customPages = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              home.homeDirectory = lib.mkOption {
                type = lib.types.str;
                default = "/example/home";
              };
              warnings = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
            };
          }
        )
        {
          phillipgreenii.programs.pg-desk = {
            enable = true;
            selfLogin = "example-login";
          }
          // desk;
          phillipgreenii.programs.pg-connector = connector;
        }
      ];
    }).config;

  summarize = c: {
    yaml = c.xdg.configFile."pg-desk/config.yaml".source;
    failedAssertions = map (a: a.message) (lib.filter (a: !a.assertion) c.assertions);
  };

  # The Jira daemon's instance (jira.enable), a second issue backend that does
  # NOT own its changes, and a pr backend whose watched query belongs to the
  # pr flow.
  jiraInstance = "watched-demo-jira";
  otherInstance = "watched-demo-other";
  prInstance = "watched-demo-pr";

  mixedBackends = {
    enable = true;
    connector.issue = [
      jiraInstance
      otherInstance
    ];
    connector.pr = [ prInstance ];
    jira = {
      enable = true;
      supervisor = "none";
      instance = jiraInstance;
      pjiraPath = "/example/bin/pjira";
    };
    backends = {
      ${jiraInstance}.queries = {
        watched-demo-query = {
          expr = "example-expr-a";
          watched = true;
        };
        unwatched-demo-shorthand = "example-expr-b";
        unwatched-demo-list = [
          "example-expr-c"
          "example-expr-d"
        ];
        unwatched-demo-object = {
          expr = "example-expr-e";
          watched = false;
        };
      };
      ${otherInstance}.queries = {
        watched-demo-other-query = {
          expr = "example-expr-f";
          watched = true;
        };
      };
      ${prInstance}.queries = {
        watched-demo-pr-query = {
          expr = "example-expr-g";
          watched = true;
        };
      };
    };
  };
in
{
  inherit jiraInstance otherInstance;

  # Watched and unwatched queries across the three backends above.
  mixed = summarize (evaluate {
    connector = mixedBackends;
  });

  # The same, plus manual names: one already derived, one not.
  mixedWithManual = summarize (evaluate {
    connector = mixedBackends;
    desk.watch.issue.queries = [
      "watched-demo-query"
      "manual-demo-query"
    ];
  });

  # The connector module disabled: nothing derived, the manual list alone.
  connectorOff = summarize (evaluate {
    connector = mixedBackends // {
      enable = false;
    };
    desk.watch.issue.queries = [ "manual-demo-query" ];
  });

  # The connector module disabled and no manual list: no watch block at all.
  connectorOffNoManual = summarize (evaluate {
    connector = mixedBackends // {
      enable = false;
    };
  });

  # The connector enabled with no watched query anywhere: no watch block.
  noneWatched = summarize (evaluate {
    connector = {
      enable = true;
      connector.issue = [ otherInstance ];
      backends.${otherInstance}.queries.unwatched-demo-shorthand = "example-expr-b";
    };
  });

  # One query name watched under two issue backends: an assertion fails.
  collision = summarize (evaluate {
    connector = {
      enable = true;
      connector.issue = [
        jiraInstance
        otherInstance
      ];
      backends = {
        ${jiraInstance}.queries.shared-demo-query = {
          expr = "example-expr-a";
          watched = true;
        };
        ${otherInstance}.queries.shared-demo-query = {
          expr = "example-expr-b";
          watched = true;
        };
      };
    };
  });
}
