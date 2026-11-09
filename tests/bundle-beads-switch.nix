# Eval-level test of the beads switch (design note
# docs/superpowers/specs/2026-11-07-mod-bundle-grouping-design.md, "beads", and the plan
# docs/superpowers/plans/2026-11-09-beads-switch-enables-plugins.md): with
# phillipgreenii.programs.beads.enable off the pb plugin is not enabled and pgd's
# components.beads is off; with it on both are on; an explicit pb.enable or
# components.beads setting wins over the new default. With Claude Code and beads on, every
# beads-family plugin resolves enabled; with beads off they resolve disabled; the agent-support
# bundle reaches the same result through the capability chain, and the beads capability veto
# turns them off again. The pb and pgd modules, the marketplaces module and the capabilities
# are the real ones, and the plugin list is built from the REAL plugin.json manifests; the
# beads module (it pulls packages) and the home-manager options are stubbed. Consumed by
# checks.<system>.test-bundle-beads-switch. All values are EXAMPLE values.
{
  lib,
  pkgs,
  mkCapability,
  mkBundle,
  framework,
}:
let
  marketplaceName = "example-marketplace-local";

  # The six beads-family plugins plus pb and session-mode (the plugin with no beads
  # requirement). Only these are listed, and every feature they require is declared below,
  # so an undeclared feature cannot hide a wrong `true`.
  beadsFamily = [
    "beads-lifecycle"
    "session-wrapup"
    "wayfinder-beads"
    "beads-dolt-doctor"
    "bead-grooming"
    "plan-decompose"
  ];
  pluginNames = beadsFamily ++ [
    "pb"
    "session-mode"
  ];

  # The stand-in marketplace's plugin list is read from the real manifests, so `requires`
  # and `defaultEnabled` cannot drift from the shipped plugins.
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

  # A built-marketplace stand-in carrying the passthru the marketplaces module reads.
  marketplace = pkgs.runCommand "example-marketplace" {
    passthru = { inherit marketplaceName plugins; };
  } "mkdir $out";

  keyOf = name: "${name}@${marketplaceName}";

  # Stubs of the home-manager options the real modules write.
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
        packages = lib.mkOption {
          type = lib.types.listOf lib.types.package;
          default = [ ];
        };
        sessionVariables = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
        };
      };
    };

  # Feature options: each declared exactly ONCE. session-mode and handoff-create default
  # from claude-code.enable, as the real modules do. claude-code is the stub (the real
  # module pulls packages); the marketplaces module contributes its own sub-options.
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
        # stub of home/programs/beads (the real one installs packages)
        beads.enable = lib.mkEnableOption "beads";
        # stubs of the worktree CLIs the real worktree-tools module turns on; pb and
        # session-wrapup require wtdone
        wtnew.enable = lib.mkEnableOption "wtnew (stub)";
        wtdone.enable = lib.mkEnableOption "wtdone (stub)";
        wtaudit.enable = lib.mkEnableOption "wtaudit (stub)";
        session-mode.enable = fromClaude;
        handoff-create.enable = fromClaude;
        claude-code = {
          enable = lib.mkEnableOption "claude-code";
          settings = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
          };
        };
      };
    };

  # The extra feature options the chain's capability leaves set (declared once each).
  chainStub =
    { lib, ... }:
    {
      options.phillipgreenii = {
        pn.enable = lib.mkEnableOption "pn (stub)";
        programs = {
          claude-extended-tool-approver.enable = lib.mkEnableOption "ceta (stub)";
          perles.enable = lib.mkEnableOption "perles (stub)";
        }
        # Features of the capability leaves this test never turns on. The module system
        # checks every definition under a leaf even when its `mkIf` is false, so each feature
        # a leaf names needs a declared option; each is still declared once.
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
    ../home/programs/pb/default.nix
    ../home/programs/pgd/default.nix
    ../home/programs/worktree-tools/default.nix
    ../home/programs/claude-marketplaces/default.nix
  ];

  evaluate =
    {
      beads ? null,
      claudeCode ? true,
      pb ? { },
      pgd ? { },
      overrides ? { },
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
              marketplaces = {
                nixProvided = [ marketplace ];
                inherit overrides;
              };
            };
            inherit pb pgd;
          }
          // lib.optionalAttrs (beads != null) { beads.enable = beads; };
        }
      ];
    };

  # The chain: the real capabilities and framework, so the switch is reached through
  # bundles.agent-support.enable. No account.isHuman is needed (only perles uses it).
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
          {
            phillipgreenii.programs.claude-code = {
              enable = true;
              marketplaces.nixProvided = [ marketplace ];
            };
          }
        ]
        ++ extra;
    };

  enabledPlugins = c: c.config.phillipgreenii.programs.claude-code.settings.enabledPlugins or { };
  pluginOn = c: name: (enabledPlugins c).${keyOf name};
  pbPluginOn = c: pluginOn c "pb";
  allOf = value: c: lib.all (n: pluginOn c n == value) beadsFamily;
  pgdBeads =
    c: (builtins.fromJSON c.config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS).components.beads;

  beadsOff = evaluate { };
  beadsOffExplicit = evaluate { beads = false; };
  beadsOn = evaluate { beads = true; };
  pbForcedOff = evaluate {
    beads = true;
    pb.enable = false;
  };
  pbForcedOn = evaluate { pb.enable = true; };
  pgdForcedOff = evaluate {
    beads = true;
    pgd.components.beads = false;
  };
  pgdForcedOn = evaluate { pgd.components.beads = true; };

  oneOverridden = evaluate {
    beads = true;
    overrides.${keyOf "beads-lifecycle"} = false;
  };
  overrideCannotEnable = evaluate {
    beads = false;
    overrides.${keyOf "beads-lifecycle"} = true;
  };
  claudeOff = evaluate {
    beads = true;
    claudeCode = false;
  };

  chainBundle = evaluateChain [ { phillipgreenii.bundles.agent-support.enable = true; } ];
  chainVeto = evaluateChain [
    {
      phillipgreenii.bundles.agent-support.enable = true;
      phillipgreenii.capabilities.beads.enable = false;
    }
  ];
  chainNoBundle = evaluateChain [ ];

  failuresOf = cs: builtins.attrNames (lib.filterAttrs (_: ok: !ok) cs);

  cases = {
    # ---- beads off (unset and explicit): pb plugin and pgd beads component are off ----
    pbPluginOffWithBeadsOff = pbPluginOn beadsOff == false && pbPluginOn beadsOffExplicit == false;
    pbPackageAbsentWithBeadsOff = beadsOff.config.home.packages == [ ];
    pgdBeadsOffWithBeadsOff = pgdBeads beadsOff == false && pgdBeads beadsOffExplicit == false;

    # ---- beads on: both are on --------------------------------------------------
    pbPluginOnWithBeadsOn = pbPluginOn beadsOn == true;
    pbPackageWithBeadsOn = beadsOn.config.home.packages == [ pkgs.pb ];
    pgdBeadsOnWithBeadsOn = pgdBeads beadsOn == true;

    # ---- an explicit setting wins ----------------------------------------------
    explicitPbOffWins = pbPluginOn pbForcedOff == false;
    explicitPbOnWins = pbPluginOn pbForcedOn == true;
    explicitPgdBeadsOffWins = pgdBeads pgdForcedOff == false;
    explicitPgdBeadsOnWins = pgdBeads pgdForcedOn == true;

    # ---- the other pgd components keep their own default ------------------------
    otherComponentsStayOn =
      let
        comps =
          (builtins.fromJSON beadsOff.config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS).components;
      in
      lib.all (n: comps.${n}) (lib.filter (n: n != "beads") (builtins.attrNames comps));

    # ---- the manifest is real: the stand-in lists exactly the eight plugins ----------
    manifestCoversPlugins =
      map (p: p.name) plugins == pluginNames
      && lib.all (n: builtins.elem "beads" (manifest n).requires) beadsFamily;

    # (a) Claude Code on and beads on: the six beads-family plugins resolve true.
    a_beadsOnSixPluginsOn = allOf true beadsOn;

    # (b) beads off: the six resolve false, while session-mode (no beads requirement) stays true.
    b_beadsOffSixPluginsOff = allOf false beadsOff && allOf false beadsOffExplicit;
    b_beadsOffSessionModeStaysOn = pluginOn beadsOff "session-mode" == true;
    # an override cannot enable a plugin whose required feature is off
    b_overrideCannotEnableWithBeadsOff = pluginOn overrideCannotEnable "beads-lifecycle" == false;

    # (c) beads on and one override false: only that plugin is false (selective control).
    c_overrideTurnsOffOnlyThatPlugin =
      pluginOn oneOverridden "beads-lifecycle" == false
      && lib.all (n: pluginOn oneOverridden n == true) (lib.remove "beads-lifecycle" beadsFamily);

    # (d) Claude Code off: nothing is registered.
    d_claudeOffNoPlugins = enabledPlugins claudeOff == { } && claudeOff.config.home.file == { };

    # (e) the chain: bundle on, nothing else set, the six plugins resolve true.
    e_bundleEnablesBeads = chainBundle.config.phillipgreenii.programs.beads.enable == true;
    e_bundleSixPluginsOn = allOf true chainBundle;
    # with no bundle the chain leaves beads off and the six plugins off (new-machine default)
    e_noBundleSixPluginsOff = allOf false chainNoBundle;

    # (f) veto: bundle on and capabilities.beads.enable = false: the six resolve false. The pb
    # plugin stays true here, because the bundle's separate pb leaf still sets pb.enable (and
    # the pb plugin requires only pb and session-mode); this is NOT "pb is off".
    f_vetoSixPluginsOff = allOf false chainVeto;
    f_vetoPbPluginStaysOn = pbPluginOn chainVeto == true;
  };

  # Harness self-test: a fixture whose expected value deliberately does NOT match must be
  # reported by the harness as a failure (a clean report here would mean failures go unseen).
  negativeFixture = {
    deliberateFailure = pluginOn beadsOff "beads-lifecycle" == true;
    deliberatePass = pluginOn beadsOff "beads-lifecycle" == false;
  };
  harnessCases = {
    harnessReportsDeliberateFailure = failuresOf negativeFixture == [ "deliberateFailure" ];
  };

  allCases = cases // harnessCases;
in
{
  cases = allCases;
  inherit negativeFixture;
  failures = failuresOf allCases;
}
