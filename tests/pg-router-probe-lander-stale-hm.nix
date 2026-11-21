# Eval-level test of the scheduled lander-stale agent in
# home/programs/pg-router-probe: the launchd entry the module hands the
# registry (tests/launchd-registry-stub.nix cannot render a plist), its
# threshold default of 2h and its options. Consumed by
# checks.<system>.test-pg-router-probe-lander-stale-hm.
{ lib, pkgs }:
let
  withPlatform =
    isDarwin:
    pkgs
    // {
      stdenv = pkgs.stdenv // {
        hostPlatform = pkgs.stdenv.hostPlatform // {
          inherit isDarwin;
        };
      };
    };

  stubs =
    { lib, ... }:
    {
      options = {
        assertions = lib.mkOption {
          type = lib.types.listOf lib.types.anything;
          default = [ ];
        };
        home = {
          packages = lib.mkOption {
            type = lib.types.listOf lib.types.package;
            default = [ ];
          };
          homeDirectory = lib.mkOption {
            type = lib.types.str;
            default = "/example/home";
          };
          username = lib.mkOption {
            type = lib.types.str;
            default = "example";
          };
          profileDirectory = lib.mkOption {
            type = lib.types.str;
            default = "/etc/profiles/per-user/example";
          };
        };
        xdg = {
          stateHome = lib.mkOption {
            type = lib.types.str;
            default = "/example/home/.local/state";
          };
          configHome = lib.mkOption {
            type = lib.types.str;
            default = "/example/home/.config";
          };
        };
      };
    };

  evalWith =
    {
      isDarwin ? true,
      declareLaunchd ? true,
      ls ? { },
    }:
    (lib.evalModules {
      specialArgs = {
        pkgs = withPlatform isDarwin;
        inherit lib;
      };
      modules = [
        ../home/programs/pg-router-probe/default.nix
        stubs
        {
          phillipgreenii.programs.pg-router-probe = {
            enable = true;
            landerStale = {
              enable = true;
              repos = [
                "/src/a"
                "/src/b"
              ];
            };
          };
        }
        { phillipgreenii.programs.pg-router-probe.landerStale = ls; }
      ]
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix;
    }).config;

  defaultC = evalWith { };
  agents = c: c.phillipgreenii.programs.launchdServices.userAgents;
  agent = c: (agents c).pg-router-lander-stale;
  has = needle: hay: lib.hasInfix needle hay;
  failedAssertions = c: lib.filter (a: !a.assertion) c.assertions;

  cases = [
    {
      name = "the agent runs lander-stale over every repo with a 2h threshold by default";
      ok =
        let
          s = (agent defaultC).script;
        in
        has "lander-stale" s
        && has "--stale-after 2h" s
        && has "--repo /src/a" s
        && has "--repo /src/b" s
        && has "--connector-backend pg-connector-issue-beads" s;
    }
    {
      name = "the threshold and interval are module config";
      ok =
        let
          c = evalWith {
            ls = {
              threshold = "6h";
              intervalSeconds = 900;
            };
          };
        in
        has "--stale-after 6h" (agent c).script && (agent c).serviceConfig.StartInterval == 900;
    }
    {
      name = "the agent is not keep-alive, has the label and a PATH with /usr/bin";
      ok =
        (agent defaultC).keepAlive == false
        && (agent defaultC).label == "com.phillipg.pg-router-lander-stale"
        && has "/usr/bin" (agent defaultC).serviceConfig.EnvironmentVariables.PATH;
    }
    {
      name = "disabled lander-stale produces no entry";
      ok =
        !(
          (agents (evalWith {
            ls.enable = lib.mkForce false;
          }))
            ? pg-router-lander-stale
        );
    }
    {
      name = "enabling with no repos fails an assertion";
      ok =
        failedAssertions (evalWith {
          ls.repos = lib.mkForce [ ];
        }) != [ ];
    }
    {
      name = "without the launchd registry, or off darwin, the module evaluates with no entry";
      ok =
        (evalWith { declareLaunchd = false; }).home.packages != [ ]
        &&
          agents (evalWith {
            isDarwin = false;
          }) == { };
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
}
