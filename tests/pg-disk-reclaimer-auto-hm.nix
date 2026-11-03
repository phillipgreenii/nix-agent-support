# Eval-level test of the scheduled `auto` job and the grower registry items of
# home/programs/pg-disk-reclaimer. Consumed by
# checks.<system>.test-pg-disk-reclaimer-auto-hm. The real module is evaluated against
# stubbed home-manager options and personal's HM launchd registry option
# (tests/launchd-registry-stub.nix); the stub cannot render a plist, so these cases pin
# the entry the module hands the registry, not what launchd does with it.
#
# Returns { failures; caseCount; registry; }: `registry` is the rendered registry.json
# of the default configuration, which the check validates with the real CLI.
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
        programs.tldr = {
          enable = lib.mkOption {
            type = lib.types.bool;
            default = false;
          };
          customPages = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
          };
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
          configFile = lib.mkOption {
            type = lib.types.attrsOf (
              lib.types.submodule { options.source = lib.mkOption { type = lib.types.path; }; }
            );
            default = { };
          };
        };
        assertions = lib.mkOption {
          type = lib.types.listOf lib.types.unspecified;
          default = [ ];
        };
      };
    };

  evalWith =
    {
      isDarwin ? true,
      declareLaunchd ? true,
      cfg,
    }:
    (lib.evalModules {
      specialArgs = {
        pkgs = withPlatform isDarwin;
        inherit lib;
      };
      modules = [
        ../home/programs/pg-disk-reclaimer/default.nix
        stubs
        { phillipgreenii.programs.pg-disk-reclaimer.enable = true; }
        cfg
      ]
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix;
    }).config;

  autoOn.phillipgreenii.programs.pg-disk-reclaimer.auto.enable = true;

  defaultC = evalWith { cfg = { }; };
  autoC = evalWith { cfg = autoOn; };
  customC = evalWith {
    cfg.imports = [
      autoOn
      {
        phillipgreenii.programs.pg-disk-reclaimer.auto = {
          thresholds = {
            level1BelowGb = 100;
            level2BelowGb = 80;
            level3BelowGb = 50;
          };
          intervalSeconds = 300;
        };
      }
    ];
  };
  badOrderC = evalWith {
    cfg.imports = [
      autoOn
      { phillipgreenii.programs.pg-disk-reclaimer.auto.thresholds.level3BelowGb = 90; }
    ];
  };
  noGrowersC = evalWith {
    cfg.phillipgreenii.programs.pg-disk-reclaimer.growerEntries.enable = false;
  };
  noRegistryC = evalWith {
    declareLaunchd = false;
    cfg = autoOn;
  };
  linuxC = evalWith {
    isDarwin = false;
    cfg = autoOn;
  };

  agents = c: c.phillipgreenii.programs.launchdServices.userAgents;
  agent = c: (agents c).pg-disk-reclaimer-auto;
  failedAssertions = c: lib.filter (a: !a.assertion) c.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  has = needle: hay: lib.hasInfix needle hay;
  registryJson =
    c: builtins.fromJSON (builtins.readFile c.xdg.configFile."pg-disk-reclaimer/registry.json".source);
  ids = c: map (i: i.id) (registryJson c);
  item = c: id: lib.findFirst (i: i.id == id) null (registryJson c);
  levels = c: id: map (v: v.aggressiveness) (item c id).variants;

  cases = [
    {
      name = "auto is off by default: no agent, no failure";
      ok = agents defaultC == { } && failedAssertions defaultC == [ ];
    }
    {
      name = "auto on: one agent labelled com.phillipg.pg-disk-reclaimer-auto";
      ok =
        lib.attrNames (agents autoC) == [ "pg-disk-reclaimer-auto" ]
        && (agent autoC).label == "com.phillipg.pg-disk-reclaimer-auto";
    }
    {
      name = "the script runs the absolute store path with --apply and the default 60/45/30 thresholds";
      ok =
        has "exec /nix/store/" (agent autoC).script
        && has "pg-disk-reclaimer auto --apply" (agent autoC).script
        && has "--below1-gb 60" (agent autoC).script
        && has "--below2-gb 45" (agent autoC).script
        && has "--below3-gb 30" (agent autoC).script
        && !(has "--aggressiveness" (agent autoC).script);
    }
    {
      name = "thresholds and interval are operator-adjustable";
      ok =
        has "--below1-gb 100" (agent customC).script
        && has "--below2-gb 80" (agent customC).script
        && has "--below3-gb 50" (agent customC).script
        && (agent customC).serviceConfig.StartInterval == 300
        && (agent autoC).serviceConfig.StartInterval == 900;
    }
    {
      name = "a job that exits between runs: keepAlive and healthCheck off, runAtLoad on";
      ok =
        (agent autoC).keepAlive == false
        && (agent autoC).healthCheck == false
        && (agent autoC).runAtLoad == true;
    }
    {
      name = "stdout is the JSON-lines run log under the XDG state home, stderr its own file";
      ok =
        (agent autoC).serviceConfig.StandardOutPath
        == "/example/home/.local/state/pg-disk-reclaimer/auto.jsonl"
        &&
          (agent autoC).serviceConfig.StandardErrorPath
          == "/example/home/.local/state/pg-disk-reclaimer/launchd-stderr.log";
    }
    {
      name = "the environment carries HOME, XDG_CONFIG_HOME and a PATH";
      ok =
        (agent autoC).serviceConfig.EnvironmentVariables.HOME == "/example/home"
        && (agent autoC).serviceConfig.EnvironmentVariables.XDG_CONFIG_HOME == "/example/home/.config"
        && has "/etc/profiles/per-user/example/bin" (agent autoC).serviceConfig.EnvironmentVariables.PATH;
    }
    {
      name = "thresholds out of order fail an assertion";
      ok =
        lib.any (m: has "level1BelowGb >= level2BelowGb >= level3BelowGb" m) (messages badOrderC)
        && failedAssertions autoC == [ ];
    }
    {
      name = "without the launchd registry on darwin an assertion fires instead of an eval error";
      ok = lib.any (m: has "needs phillipgreenii.programs.launchdServices" m) (messages noRegistryC);
    }
    {
      name = "a non-darwin host gets no agent and no failure";
      ok = agents linuxC == { } && failedAssertions linuxC == [ ];
    }
    {
      name = "the four grower items are in the registry by default and absent when disabled";
      ok =
        lib.all (id: lib.elem id (ids defaultC)) [
          "pg-claude-code-recorder-store"
          "claude-projects"
          "pg2-scratch-caches"
          "library-caches"
        ]
        && ids noGrowersC == [ ];
    }
    {
      name = "the recorder store item only calls the recorder's own retention command";
      ok =
        let
          v = lib.head (item defaultC "pg-claude-code-recorder-store").variants;
        in
        v.removeCommand == "pg-claude-code-recorder remove --all-eligible --yes"
        && v.dryRunCommand == "pg-claude-code-recorder remove --dry-run";
    }
    {
      name = "scheduled levels (1 to 3) never include the irreversible transcript deletion (level 4)";
      ok =
        levels defaultC "claude-projects" == [ 4 ]
        && levels defaultC "pg2-scratch-caches" == [ 2 ]
        && levels defaultC "library-caches" == [ 2 ]
        && levels defaultC "pg-claude-code-recorder-store" == [ 3 ];
    }
    {
      name = "every grower removal has an age cutoff";
      ok = lib.all (id: has "time +" (lib.head (item defaultC id).variants).removeCommand) [
        "claude-projects"
        "pg2-scratch-caches"
        "library-caches"
      ];
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
  registry = defaultC.xdg.configFile."pg-disk-reclaimer/registry.json".source;
}
