# Eval-level test of the tmp-sweep home-manager module (bead pg2-bo3n7, packet C of
# the build_tmp design): the daily launchd user agent that sweeps the build-tmp and
# claude-code-tmp roots through pg-disk-reclaimer. Consumed by
# checks.<system>.test-tmp-sweep in flake.nix, which also runs tests/tmp-sweep.bats
# over the rendered artifacts (`artifacts` below).
#
# The module is evaluated with the REAL build-tmp, claude-code-tmp and
# pg-disk-reclaimer modules, so the registry the agent's command line runs against is
# the real one. What this flake does not declare is STUBBED: home-manager's zsh/bash
# options, home.activation, lib.hm.dag, the directory options, and personal's HM
# launchd registry option (tests/launchd-registry-stub.nix, the `declareLaunchd`
# switch). The stub CANNOT render a real plist: the plist keys, including the
# StartCalendarInterval pass-through into it, are only exercised at the terminal
# flake. These cases pin the entry the module hands the registry (script, label,
# serviceConfig), not what launchd would do with it.
#
# Returns { failures; caseCount; artifacts; }. The artifacts are the rendered agent
# scripts (both roots, build-tmp only, claude-code-tmp only) and the matching
# registry.json files, with the roots spelled /ROOT_PLACEHOLDER/<basename>, for the
# bats suite to point at throwaway fixture roots.
{ lib, pkgs }:
let
  hmLib = lib // {
    hm = (lib.hm or { }) // {
      dag = (lib.hm.dag or { }) // {
        entryAfter = deps: text: { inherit deps text; };
      };
    };
  };

  # The module reads the platform from pkgs; a Linux builder also runs this check,
  # so the platform is presented explicitly (as tests/pg-keep-awake-hm-launchd.nix).
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
        programs = {
          zsh.enable = lib.mkOption {
            type = lib.types.bool;
            default = false;
          };
          zsh.envExtra = lib.mkOption {
            type = lib.types.lines;
            default = "";
          };
          bash = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = false;
            };
            profileExtra = lib.mkOption {
              type = lib.types.lines;
              default = "";
            };
            bashrcExtra = lib.mkOption {
              type = lib.types.lines;
              default = "";
            };
          };
          tldr = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = false;
            };
            customPages = lib.mkOption {
              type = lib.types.attrsOf lib.types.anything;
              default = { };
            };
          };
        };
        home = {
          activation = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
          };
          packages = lib.mkOption {
            type = lib.types.listOf lib.types.package;
            default = [ ];
          };
          homeDirectory = lib.mkOption {
            type = lib.types.str;
            default = "/example/home";
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
      reclaimer ? true,
      cfg,
    }:
    (lib.evalModules {
      specialArgs = {
        pkgs = withPlatform isDarwin;
        lib = hmLib;
      };
      modules = [
        ../home/programs/build-tmp/default.nix
        ../home/programs/claude-code-tmp/default.nix
        ../home/programs/pg-disk-reclaimer/default.nix
        ../home/programs/tmp-sweep/default.nix
        stubs
        { phillipgreenii.programs.pg-disk-reclaimer.enable = reclaimer; }
        cfg
      ]
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix;
    }).config;

  buildOn.phillipgreenii.programs.build-tmp = {
    enable = true;
    path = "/ROOT_PLACEHOLDER/build_tmp";
  };
  claudeOn.phillipgreenii.programs.claude-code-tmp = {
    enable = true;
    path = "/ROOT_PLACEHOLDER/claude_code_tmp";
  };
  both.imports = [
    buildOn
    claudeOn
  ];

  bothC = evalWith { cfg = both; };
  buildC = evalWith { cfg = buildOn; };
  claudeC = evalWith { cfg = claudeOn; };
  neither = evalWith { cfg = { }; };
  # the sweep forced on although no root is enabled: still no agent
  forcedNoRoots = evalWith { cfg.phillipgreenii.programs.tmp-sweep.enable = true; };
  sweepOff = evalWith {
    cfg.imports = [
      both
      { phillipgreenii.programs.tmp-sweep.enable = false; }
    ];
  };
  noReclaimer = evalWith {
    reclaimer = false;
    cfg = both;
  };
  noRegistry = evalWith {
    declareLaunchd = false;
    cfg = both;
  };
  noRegistryDisabled = evalWith {
    declareLaunchd = false;
    cfg = { };
  };
  linux = evalWith {
    isDarwin = false;
    declareLaunchd = false;
    cfg = both;
  };
  linuxWithRegistry = evalWith {
    isDarwin = false;
    cfg = both;
  };

  agents = c: c.phillipgreenii.programs.launchdServices.userAgents;
  agent = c: (agents c).tmp-sweep;
  opts = c: c.phillipgreenii.programs.tmp-sweep;
  failedAssertions = c: lib.filter (a: !a.assertion) c.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  has = needle: hay: lib.hasInfix needle hay;
  # the script's non-empty lines
  scriptLines = c: lib.filter (l: l != "") (lib.splitString "\n" (agent c).script);
  pkgBin = "${pkgs.pg-disk-reclaimer}/bin/pg-disk-reclaimer";
  lineFor = ids: "exec ${pkgBin} reclaim --aggressiveness 1 ${ids} --apply";
  sc = c: (agent c).serviceConfig;

  cases = [
    {
      name = "both roots enabled: the exact command line names both ids explicitly";
      ok = scriptLines bothC == [ (lineFor "build-tmp claude-code-tmp") ];
    }
    {
      name = "only build-tmp enabled: only its id appears";
      ok = scriptLines buildC == [ (lineFor "build-tmp") ];
    }
    {
      name = "only claude-code-tmp enabled: only its id appears";
      ok = scriptLines claudeC == [ (lineFor "claude-code-tmp") ];
    }
    {
      name = "neither root enabled: no agent, and forcing the sweep on renders none and fails nothing";
      ok =
        agents neither == { }
        && agents forcedNoRoots == { }
        && failedAssertions neither == [ ]
        && failedAssertions forcedNoRoots == [ ];
    }
    {
      name = "the sweep follows the roots: enable defaults to true when either root is enabled, false when neither";
      ok =
        !(opts neither).enable
        && (opts buildC).enable
        && (opts claudeC).enable
        && (opts bothC).enable
        && !(opts sweepOff).enable
        && agents sweepOff == { };
    }
    {
      name = "the agent key is tmp-sweep and the label com.phillipg.tmp-sweep";
      ok =
        lib.attrNames (agents bothC) == [ "tmp-sweep" ]
        && (agent bothC).label == "com.phillipg.tmp-sweep"
        && (agent buildC).label == "com.phillipg.tmp-sweep"
        && (agent claudeC).label == "com.phillipg.tmp-sweep";
    }
    {
      name = "the script runs the ABSOLUTE store path of pg-disk-reclaimer at level 1 only, with explicit ids";
      ok =
        lib.all (c: has "exec /nix/store/" (agent c).script && has "-pg-disk-reclaimer-" (agent c).script) [
          bothC
          buildC
          claudeC
        ]
        && lib.all (c: !(has "--aggressiveness 3" (agent c).script)) [
          bothC
          buildC
          claudeC
        ]
        && lib.all (c: has "--aggressiveness 1 " (agent c).script) [
          bothC
          buildC
          claudeC
        ];
    }
    {
      name = "a daily calendar interval at 03:30 passes through serviceConfig";
      ok =
        (sc bothC).StartCalendarInterval == [
          {
            Hour = 3;
            Minute = 30;
          }
        ];
    }
    {
      name = "EnvironmentVariables carry HOME and XDG_CONFIG_HOME (where the CLI reads its registry)";
      ok =
        (sc bothC).EnvironmentVariables == {
          HOME = "/example/home";
          XDG_CONFIG_HOME = "/example/home/.config";
        };
    }
    {
      name = "a job that exits between runs: keepAlive and healthCheck off, runAtLoad on, not launch-once";
      ok =
        (agent bothC).keepAlive == false
        && (agent bothC).healthCheck == false
        && (agent bothC).runAtLoad == true
        && (agent bothC).launchOnlyOnce == false;
    }
    {
      name = "stdout and stderr go to their own files under the XDG state home";
      ok =
        (sc bothC).StandardOutPath == "/example/home/.local/state/tmp-sweep/sweep.out.log"
        && (sc bothC).StandardErrorPath == "/example/home/.local/state/tmp-sweep/sweep.err.log";
    }
    {
      name = "the sweep needs pg-disk-reclaimer enabled: without it the assertion fires, naming tmp-sweep";
      ok =
        lib.any (m: has "tmp-sweep.enable requires" m && has "pg-disk-reclaimer.enable" m) (
          messages noReclaimer
        )
        && failedAssertions bothC == [ ];
    }
    {
      name = "without the launchd registry option on darwin an assertion fires instead of an eval error; disabled it stays silent";
      ok =
        lib.any (m: has "tmp-sweep.enable on darwin needs phillipgreenii.programs.launchdServices" m) (
          messages noRegistry
        )
        && failedAssertions noRegistryDisabled == [ ];
    }
    {
      name = "a non-darwin host gets no agent and no failure, with or without the registry option";
      ok =
        failedAssertions linux == [ ]
        && failedAssertions linuxWithRegistry == [ ]
        && agents linuxWithRegistry == { };
    }
  ];

  rendered = c: (agent c).script;
  registry = c: c.xdg.configFile."pg-disk-reclaimer/registry.json".source;
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
  artifacts = {
    scriptBoth = pkgs.writeText "tmp-sweep-script-both" (rendered bothC);
    scriptBuild = pkgs.writeText "tmp-sweep-script-build" (rendered buildC);
    scriptClaude = pkgs.writeText "tmp-sweep-script-claude" (rendered claudeC);
    regBoth = registry bothC;
    regBuild = registry buildC;
    regClaude = registry claudeC;
  };
}
