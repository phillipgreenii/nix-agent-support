# Eval-level test of the host-forensics home-manager module: home/default.nix must
# import it (a missing import leaves its options unreachable from any consumer) and,
# enabled, it must register the launchd user agent. Consumed by
# checks.<system>.test-host-forensics-eval in flake.nix. Same stub approach as
# tests/tmp-sweep.nix: what this flake does not declare (home.*, xdg.*, assertions,
# personal's launchd registry) is stubbed.
#
# Returns { failures; caseCount; }.
{ lib, pkgs }:
let
  darwinPkgs = pkgs // {
    stdenv = pkgs.stdenv // {
      hostPlatform = pkgs.stdenv.hostPlatform // {
        isDarwin = true;
      };
    };
  };

  stubs =
    { lib, ... }:
    {
      options = {
        home.homeDirectory = lib.mkOption {
          type = lib.types.str;
          default = "/example/home";
        };
        xdg.stateHome = lib.mkOption {
          type = lib.types.str;
          default = "/example/home/.local/state";
        };
        assertions = lib.mkOption {
          type = lib.types.listOf lib.types.unspecified;
          default = [ ];
        };
      };
    };

  evalWith =
    cfg:
    (lib.evalModules {
      specialArgs.pkgs = darwinPkgs;
      modules = [
        ../home/programs/host-forensics/default.nix
        stubs
        ./launchd-registry-stub.nix
        cfg
      ];
    }).config;

  on = evalWith { phillipgreenii.programs.host-forensics.enable = true; };
  off = evalWith { };
  agents = c: c.phillipgreenii.programs.launchdServices.userAgents;

  # the real aggregate's import list, as a consumer of homeModules.default gets it
  aggregateImports = (import ../home/default.nix { }).imports;

  cases = [
    {
      name = "home/default.nix imports the host-forensics module";
      ok = lib.elem ../home/programs/host-forensics aggregateImports;
    }
    {
      name = "enabled: the agent com.phillipg.host-forensics is registered and runs the script";
      ok =
        lib.attrNames (agents on) == [ "host-forensics" ]
        && (agents on).host-forensics.label == "com.phillipg.host-forensics"
        && lib.hasInfix "host-forensics" (agents on).host-forensics.script
        && lib.all (a: a.assertion) on.assertions;
    }
    {
      name = "disabled by default: no agent";
      ok = agents off == { };
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
}
