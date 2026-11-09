# Eval-level test of the worktree-tools switch: one toggle
# (phillipgreenii.programs.worktree-tools.enable, default claude-code.enable) turns on wtnew,
# wtdone and wtaudit at mkDefault, and the plugins that call wtdone (pb, integrate-branch,
# session-wrapup) require that exact tool, so a plugin is never enabled while its tool is off.
# The worktree-tools and marketplaces modules, the capabilities and the framework are the
# real ones, and the plugin list is built from the REAL plugin.json manifests, so a
# default -> overrides -> default cycle would fail evaluation here. The tool modules, pb,
# beads and the home-manager options are stubbed, each declared once. claude-code.enable is a
# parameter. Consumed by checks.<system>.test-worktree-tools-switch. All values are EXAMPLE values.
{
  lib,
  pkgs,
  mkCapability,
  mkBundle,
  framework,
}:
let
  marketplaceName = "example-marketplace-local";

  # The plugins that call wtdone, read from the real manifests.
  pluginNames = [
    "pb"
    "integrate-branch"
    "session-wrapup"
  ];
  manifest =
    name:
    builtins.fromJSON (builtins.readFile ../claude-marketplace/${name}/.claude-plugin/plugin.json);
  plugins = map (
    name:
    let
      m = manifest name;
    in
    {
      inherit (m) name version defaultEnabled;
      key = "${name}@${marketplaceName}";
      requires = m.requires or [ ];
    }
  ) pluginNames;
  marketplace = pkgs.runCommand "example-marketplace" {
    passthru = { inherit marketplaceName plugins; };
  } "mkdir $out";
  keyOf = name: "${name}@${marketplaceName}";

  homeStub =
    { lib, ... }:
    {
      options.home = {
        homeDirectory = lib.mkOption {
          type = lib.types.str;
          default = "/example/home";
        };
        file = lib.mkOption {
          type = lib.types.attrsOf lib.types.anything;
          default = { };
        };
      };
    };

  # Feature options, each declared exactly ONCE. The features the three plugins require
  # (besides the worktree tools) default from claude-code.enable, as the real modules do,
  # except pb and beads, which a machine or the bundle turns on.
  featureStub =
    { lib, config, ... }:
    let
      fromClaude = lib.mkOption {
        type = lib.types.bool;
        default = config.phillipgreenii.programs.claude-code.enable;
      };
    in
    {
      options.phillipgreenii.programs = {
        wtnew.enable = lib.mkEnableOption "wtnew (stub)";
        wtdone.enable = lib.mkEnableOption "wtdone (stub)";
        wtaudit.enable = lib.mkEnableOption "wtaudit (stub)";
        pb.enable = lib.mkEnableOption "pb (stub)";
        beads.enable = lib.mkEnableOption "beads (stub)";
        session-mode.enable = fromClaude;
        handoff-create.enable = fromClaude;
        integrate-branch-support.enable = fromClaude;
        claude-code = {
          enable = lib.mkEnableOption "claude-code";
          settings = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
          };
        };
      };
    };

  # Features of the capability leaves that this test never reads; each leaf feature needs a
  # declared option even under a false mkIf.
  chainStub =
    { lib, ... }:
    {
      options.phillipgreenii = {
        pn.enable = lib.mkEnableOption "pn (stub)";
        programs = {
          claude-extended-tool-approver.enable = lib.mkEnableOption "ceta (stub)";
          perles.enable = lib.mkEnableOption "perles (stub)";
        }
        // lib.genAttrs unusedLeafFeatures (n: {
          enable = lib.mkEnableOption "${n} (stub)";
        });
      };
    };
  unusedLeafFeatures = [
    "codeburn"
    "serena"
    "ollama"
    "opencode"
    "rtk"
    "git-tools"
    "tuicr"
    "ccpool"
    "pg-router"
    "pa-monitor"
    "pg-keep-awake"
    "agentsview"
  ];

  realModules = [
    ../home/programs/worktree-tools/default.nix
    ../home/programs/claude-marketplaces/default.nix
  ];

  # Cases (a) to (d), (f): the toggle and tool options set directly.
  evaluate =
    {
      claudeCode ? true,
      toggle ? null,
      tools ? { },
    }:
    lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = realModules ++ [
        homeStub
        featureStub
        {
          phillipgreenii.programs = {
            claude-code = {
              enable = claudeCode;
              marketplaces.nixProvided = [ marketplace ];
            };
            # pb and beads are on, so only the worktree tools decide the three plugins
            pb.enable = true;
            beads.enable = true;
          }
          // lib.mapAttrs (_: v: { enable = v; }) tools
          // lib.optionalAttrs (toggle != null) { worktree-tools.enable = toggle; };
        }
      ];
    };

  # Cases (e), (g): through the real capabilities and framework.
  evaluateChain =
    extra:
    lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules =
        realModules
        ++ [
          framework
          (import ../home/capabilities { inherit mkCapability mkBundle; })
          homeStub
          featureStub
          chainStub
          { phillipgreenii.programs.claude-code.marketplaces.nixProvided = [ marketplace ]; }
        ]
        ++ extra;
    };

  p = c: c.config.phillipgreenii.programs;
  toolsAre = c: v: (p c).wtnew.enable == v && (p c).wtdone.enable == v && (p c).wtaudit.enable == v;
  enabledPlugins = c: (p c).claude-code.settings.enabledPlugins or { };
  pluginsAre = c: v: lib.all (n: (enabledPlugins c).${keyOf n} == v) pluginNames;

  claudeOn = evaluate { };
  claudeOff = evaluate { claudeCode = false; };
  toggleOff = evaluate { toggle = false; };
  wtnewOff = evaluate { tools.wtnew = false; };
  wtdoneOff = evaluate { tools.wtdone = false; };
  leafOnly = evaluateChain [ { phillipgreenii.capabilities.worktree-tools.enable = true; } ];
  bundleToggleOff = evaluateChain [
    {
      phillipgreenii.bundles.agent-support.enable = true;
      phillipgreenii.programs.worktree-tools.enable = false;
    }
  ];
  bundleOn = evaluateChain [ { phillipgreenii.bundles.agent-support.enable = true; } ];

  failuresOf = cs: builtins.attrNames (lib.filterAttrs (_: ok: !ok) cs);

  cases = {
    # the stand-in lists the real manifests, and each calls wtdone as a hard requirement
    manifestsRequireWtdone = lib.all (n: builtins.elem "wtdone" (manifest n).requires) pluginNames;

    # (a) claude-code on: the toggle and the three tools are true
    a_claudeOnToggleAndTools = (p claudeOn).worktree-tools.enable == true && toolsAre claudeOn true;
    # (b) claude-code off: all false
    b_claudeOffAllOff = (p claudeOff).worktree-tools.enable == false && toolsAre claudeOff false;
    # (c) toggle false with claude-code on: the three tools are false
    c_toggleOffToolsOff = toolsAre toggleOff false;
    # (d) toggle on and wtnew = false: only wtnew is false
    d_wtnewOnlyOff =
      (p wtnewOff).wtnew.enable == false
      && (p wtnewOff).wtdone.enable == true
      && (p wtnewOff).wtaudit.enable == true;
    # (e) the capability leaf on with claude-code off: toggle and tools true
    e_leafOnClaudeOff =
      (p leafOnly).claude-code.enable == false
      && (p leafOnly).worktree-tools.enable == true
      && toolsAre leafOnly true;
    # (f) the three plugins follow the tools
    f_toolsOnPluginsOn = pluginsAre claudeOn true;
    f_wtdoneOffPluginsOff = pluginsAre wtdoneOff false;
    f_toggleOffPluginsOff = pluginsAre toggleOff false;
    # (g) bundle on plus toggle false: tools and the three plugins off
    g_bundleOnPluginsOn = pluginsAre bundleOn true;
    g_bundleToggleOffToolsOff = toolsAre bundleToggleOff false;
    g_bundleToggleOffPluginsOff = pluginsAre bundleToggleOff false;
  };

  # (h) Harness self-test: a fixture whose expectation deliberately does NOT hold must be
  # reported by the harness as a failure.
  negativeFixture = {
    deliberateFailure = toolsAre toggleOff true;
    deliberatePass = toolsAre toggleOff false;
  };
  harnessCases = {
    h_harnessReportsDeliberateFailure = failuresOf negativeFixture == [ "deliberateFailure" ];
  };

  allCases = cases // harnessCases;
in
{
  cases = allCases;
  inherit negativeFixture;
  failures = failuresOf allCases;
}
