# Eval-level test of the pg-disk-reclaimer REGISTRY ITEMS contributed by the
# build-tmp and claude-code-tmp modules (bead pg2-rym7f, packet B of the build_tmp
# design). Unlike tests/tmp-root-modules.nix (which stubs the reclaimer), this
# evaluates the REAL home/programs/pg-disk-reclaimer module, so the item is checked
# against the actual option schema and rendered into the real registry.json that
# tests/tmp-root-registry.bats feeds to the real CLI.
#
# Returns { failures; caseCount; artifacts; }. The artifacts are rendered registry
# files with the root spelled /ROOT_PLACEHOLDER/<basename>.
{ lib, pkgs }:
let
  hmLib = lib // {
    hm = (lib.hm or { }) // {
      dag = (lib.hm.dag or { }) // {
        entryAfter = deps: text: { inherit deps text; };
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
        home.activation = lib.mkOption {
          type = lib.types.attrsOf lib.types.anything;
          default = { };
        };
        home.packages = lib.mkOption {
          type = lib.types.listOf lib.types.package;
          default = [ ];
        };
        xdg.configFile = lib.mkOption {
          type = lib.types.attrsOf (
            lib.types.submodule { options.source = lib.mkOption { type = lib.types.path; }; }
          );
          default = { };
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
      specialArgs = {
        inherit pkgs;
        lib = hmLib;
      };
      modules = [
        ../home/programs/build-tmp/default.nix
        ../home/programs/claude-code-tmp/default.nix
        ../home/programs/pg-disk-reclaimer/default.nix
        stubs
        { phillipgreenii.programs.pg-disk-reclaimer.enable = true; }
        cfg
      ];
    }).config;

  buildPath = "/ROOT_PLACEHOLDER/build_tmp";
  claudePath = "/ROOT_PLACEHOLDER/claude_code_tmp";
  buildOn.phillipgreenii.programs.build-tmp = {
    enable = true;
    path = buildPath;
  };
  claudeOn.phillipgreenii.programs.claude-code-tmp = {
    enable = true;
    path = claudePath;
  };

  none = evalWith { };
  buildOnly = evalWith buildOn;
  claudeOnly = evalWith claudeOn;
  both = evalWith {
    imports = [
      buildOn
      claudeOn
    ];
  };
  tuned = evalWith {
    imports = [
      buildOn
      claudeOn
      {
        phillipgreenii.programs.build-tmp = {
          level1Minutes = 30;
          level3Minutes = 7;
        };
        phillipgreenii.programs.claude-code-tmp.level1Minutes = 99;
      }
    ];
  };
  unsafe = evalWith {
    phillipgreenii.programs.build-tmp = {
      enable = true;
      path = "/a b/build_tmp";
    };
  };

  entries = c: c.phillipgreenii.programs.pg-disk-reclaimer.registryEntries;
  ids = c: map (e: e.id) (entries c);
  entry = c: id: lib.findFirst (e: e.id == id) (throw "no registry entry ${id}") (entries c);
  variant =
    c: id: level:
    lib.findFirst (v: v.aggressiveness == level) null (entry c id).variants;
  has = needle: hay: lib.hasInfix needle hay;
  failedAssertions = c: lib.filter (a: !a.assertion) c.assertions;
  allCommands =
    c: id:
    lib.concatMap (v: [
      v.dryRunCommand
      v.removeCommand
    ]) (entry c id).variants;
  shaped = cmds: lib.all (x: lib.hasPrefix ": ; ( . " x && lib.hasSuffix " )" x) cmds;

  cases = [
    {
      name = "each module renders exactly its own item: neither, only build-tmp, only claude-code-tmp, both";
      ok =
        ids none == [ ]
        && ids buildOnly == [ "build-tmp" ]
        && ids claudeOnly == [ "claude-code-tmp" ]
        &&
          lib.sort lib.lessThan (ids both) == [
            "build-tmp"
            "claude-code-tmp"
          ];
    }
    {
      name = "each item has exactly the level 1 and level 3 variants";
      ok =
        map (v: v.aggressiveness) (entry both "build-tmp").variants == [
          1
          3
        ]
        &&
          map (v: v.aggressiveness) (entry both "claude-code-tmp").variants == [
            1
            3
          ];
    }
    {
      name = "the variant labels read idle <N> min with the option defaults";
      ok =
        has "idle 1440 min" (variant both "build-tmp" 1).variantDescription
        && has "idle 60 min" (variant both "build-tmp" 3).variantDescription
        && has "idle 20160 min" (variant both "claude-code-tmp" 1).variantDescription
        && has "idle 2880 min" (variant both "claude-code-tmp" 3).variantDescription;
    }
    {
      name = "the commands interpolate the minutes options: changing an option changes label and command";
      ok =
        has "idle 30 min" (variant tuned "build-tmp" 1).variantDescription
        && has " 30 1 none " (variant tuned "build-tmp" 1).removeCommand
        && !(has " 1440 " (variant tuned "build-tmp" 1).removeCommand)
        && has " 7 1 none " (variant tuned "build-tmp" 3).dryRunCommand
        && has " 99 3 claude-session " (variant tuned "claude-code-tmp" 1).removeCommand
        && has " 2880 3 claude-session " (variant tuned "claude-code-tmp" 3).removeCommand;
    }
    {
      name = "every command is ':' then a ( ) subshell, dry runs use dry and removals use apply";
      ok =
        shaped (allCommands both "build-tmp")
        && shaped (allCommands both "claude-code-tmp")
        && has "pgdr_tmp_sweep dry " (variant both "build-tmp" 1).dryRunCommand
        && has "pgdr_tmp_sweep apply " (variant both "build-tmp" 1).removeCommand
        && has "pgdr_tmp_sweep dry " (variant both "claude-code-tmp" 3).dryRunCommand
        && has "pgdr_tmp_sweep apply " (variant both "claude-code-tmp" 3).removeCommand;
    }
    {
      name = "the build-tmp item sweeps depth 1 without a session guard, claude-code-tmp depth 3 with one, each with its own marker and basename";
      ok =
        has ".build-tmp-root build_tmp 1440 1 none" (variant both "build-tmp" 1).removeCommand
        &&
          has ".claude-code-tmp-root claude_code_tmp 2880 3 claude-session"
            (variant both "claude-code-tmp" 3).removeCommand;
    }
    {
      name = "the library is sourced by absolute store path and the path is the module's own root";
      ok =
        has "-pgdr-tmp-sweep.bash" (variant both "build-tmp" 1).removeCommand
        && has ". /nix/store/" (variant both "build-tmp" 1).removeCommand
        && (entry both "build-tmp").path == buildPath
        && (entry both "claude-code-tmp").path == claudePath;
    }
    {
      name = "sizing is bounded: a raised display timeout and an upper_bound size kind with its basis";
      ok =
        (entry both "build-tmp").displayTimeoutSeconds == 600
        && (entry both "build-tmp").sizeKind == "upper_bound"
        && (entry both "build-tmp").sizeBasis != null
        && (entry both "claude-code-tmp").displayTimeoutSeconds == 600;
    }
    {
      name = "a path with a space (expanded unquoted by the CLI) fails an assertion";
      ok =
        lib.any (a: has "may only contain" a.message) (failedAssertions unsafe)
        && failedAssertions both == [ ];
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
  artifacts = {
    regBoth =
      (evalWith {
        imports = [
          buildOn
          claudeOn
        ];
      }).xdg.configFile."pg-disk-reclaimer/registry.json".source;
    regBuild = buildOnly.xdg.configFile."pg-disk-reclaimer/registry.json".source;
    regClaude = claudeOnly.xdg.configFile."pg-disk-reclaimer/registry.json".source;
  };
}
