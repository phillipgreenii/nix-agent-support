# Eval-only tests for the single-boolean ties (bead tc-2k1o8, epic tc-ys2l8 D3/D4):
#
#   phillipgreenii.pn.enable  --default-->  programs.pnwf.enable, programs.wsplan.enable
#   programs.pnwf.enable      --override--> pn-workspace-rules@<repo-base mkt> plugin
#   programs.claude-code.enable --default--> programs.integrate-branch-support.enable
#   programs.integrate-branch-support.enable --override--> integrate-branch@<this mkt>
#   capabilities.pn-workspace.enable / bundles.agent-support.enable --mkDefault--> pn.enable
#
# The REAL claude-marketplaces module is evaluated, so a cycle between the enable
# defaults and the `marketplaces.overrides` they feed would fail evaluation here
# (infinite recursion), and the plugin state is read from the resolved
# `settings.enabledPlugins`, i.e. what actually gets registered. Only the declaration of
# `phillipgreenii.pn.enable` (nix-repo-base's home/pn module) and the claude-code
# settings surface are stubbed. Consumed by checks.<system>.test-pnwf-enable-default,
# test-wsplan-enable-default, test-integrate-branch-support-enable-default and
# test-pn-single-boolean-ties, each of which fails listing every row that is false.
{
  lib,
  pkgs,
  capabilityFramework,
  capabilities,
}:
let
  mkMarketplace =
    name: pluginName: defaultEnabled:
    pkgs.runCommand "mock-${pluginName}-marketplace" {
      passthru = {
        marketplaceName = name;
        plugins = [
          {
            name = pluginName;
            version = "0.1.0+aaaaaaaa";
            key = "${pluginName}@${name}";
            inherit defaultEnabled;
          }
        ];
      };
    } "mkdir -p $out/.claude-plugin; echo '{}' > $out/.claude-plugin/marketplace.json";

  # The real marketplace names and plugin keys (verified against the built
  # marketplaces' passthru). defaultEnabled = true like the real plugins, so a row
  # that expects "off" can only get there through the override.
  repoBaseMkt = mkMarketplace "phillipg-nix-repo-base-marketplace-local" "pn-workspace-rules" true;
  ownMkt = mkMarketplace "phillipgreenii-nix-agent-support-marketplace-local" "integrate-branch" true;
  pnKey = "pn-workspace-rules@phillipg-nix-repo-base-marketplace-local";
  ibKey = "integrate-branch@phillipgreenii-nix-agent-support-marketplace-local";

  bothPkgs = pkgs // {
    pnwf = pkgs.hello;
    wsplan = pkgs.hello;
  };

  # Feature flags the agent-support bundle's capabilities set, stubbed so the
  # bundle can be enabled without importing those feature modules.
  otherFeatures = [
    "agentsview"
    "beads"
    "ccpool"
    "claude-extended-tool-approver"
    "codeburn"
    "git-tools"
    "ollama"
    "opencode"
    "pa-monitor"
    "pb"
    "perles"
    "pg-keep-awake"
    "pg-router"
    "rtk"
    "serena"
    "tuicr"
    "wtaudit"
    "wtdone"
    "wtnew"
    "worktree-tools"
  ];

  eval =
    evalPkgs: cfg:
    (lib.evalModules {
      specialArgs = {
        pkgs = evalPkgs;
        inherit lib;
      };
      modules = [
        capabilityFramework
        capabilities
        ../home/programs/claude-marketplaces/default.nix
        ../home/programs/pnwf/default.nix
        ../home/programs/wsplan/default.nix
        ../home/programs/integrate-branch-support/default.nix
        (
          { lib, ... }:
          {
            options = {
              # Declared by nix-repo-base's home/pn module in the real composition.
              phillipgreenii = {
                pn.enable = lib.mkEnableOption "pn (stub)";
                programs.claude-code = {
                  enable = lib.mkEnableOption "claude (stub)";
                  settings = {
                    extraKnownMarketplaces = lib.mkOption {
                      type = lib.types.attrsOf (lib.types.attrsOf lib.types.anything);
                      default = { };
                    };
                    enabledPlugins = lib.mkOption {
                      type = lib.types.attrsOf lib.types.bool;
                      default = { };
                    };
                    plugins = lib.mkOption {
                      type = lib.types.listOf lib.types.str;
                      default = [ ];
                    };
                  };
                };
              };
              home = {
                homeDirectory = lib.mkOption {
                  type = lib.types.str;
                  default = "/home/test";
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
              programs.tldr = {
                enable = lib.mkEnableOption "tldr (stub)";
                customPages = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
              };
            };
          }
        )
        (
          { lib, ... }:
          {
            options.phillipgreenii.programs = lib.genAttrs otherFeatures (_: {
              enable = lib.mkEnableOption "feature (stub)";
            });
          }
        )
        {
          phillipgreenii.programs.claude-code.marketplaces.nixProvided = [
            repoBaseMkt
            ownMkt
          ];
        }
      ]
      ++ lib.toList cfg;
    }).config;

  claudeOn = {
    phillipgreenii.programs.claude-code.enable = true;
  };
  res = c: {
    pn = c.phillipgreenii.pn.enable;
    pnwf = c.phillipgreenii.programs.pnwf.enable;
    wsplan = c.phillipgreenii.programs.wsplan.enable;
    ibs = c.phillipgreenii.programs.integrate-branch-support.enable;
    pnPlugin = c.phillipgreenii.programs.claude-code.settings.enabledPlugins.${pnKey} or null;
    ibPlugin = c.phillipgreenii.programs.claude-code.settings.enabledPlugins.${ibKey} or null;
  };
  ev = cfg: res (eval bothPkgs cfg);

  pnOff = ev claudeOn;
  # claude-code on AND pn on. Spelled out: `//` on these attrsets is shallow and would
  # replace the whole `phillipgreenii` attr, silently turning claude off.
  both = {
    phillipgreenii.pn.enable = true;
    phillipgreenii.programs.claude-code.enable = true;
  };
  pnOn = ev both;
  pnOnClaudeOff = ev { phillipgreenii.pn.enable = true; };
  pnwfVetoed = ev [
    both
    { phillipgreenii.programs.pnwf.enable = false; }
  ];
  ibsVetoed = ev [
    both
    { phillipgreenii.programs.integrate-branch-support.enable = false; }
  ];
  viaCapability = ev [
    claudeOn
    { phillipgreenii.capabilities.pn-workspace.enable = true; }
  ];
  viaBundle = ev {
    phillipgreenii.bundles.agent-support.enable = true;
  };
  # A plain assignment beats the bundle's mkDefault (machine veto keeps working).
  bundleVetoed = ev {
    phillipgreenii.bundles.agent-support.enable = true;
    phillipgreenii.pn.enable = false;
  };
  pkgsNoPnwf = res (
    eval (builtins.removeAttrs bothPkgs [
      "pnwf"
      "wsplan"
    ]) both
  );
  wsplanOnlyAbsent = res (eval (builtins.removeAttrs bothPkgs [ "wsplan" ]) both);
  explicitWsplan = ev { phillipgreenii.programs.wsplan.enable = true; };
  explicitPnwf = ev [
    claudeOn
    { phillipgreenii.programs.pnwf.enable = true; }
  ];
  explicitIbs = ev { phillipgreenii.programs.integrate-branch-support.enable = true; };

  row = name: ok: { inherit name ok; };
  rows = {
    pnwf = [
      (row "pn off: pnwf off" (pnOff.pnwf == false))
      (row "pn off: pn-workspace-rules plugin off (tied to pnwf)" (pnOff.pnPlugin == false))
      (row "pn on: pnwf on" (pnOn.pnwf == true))
      (row "pn on: pn-workspace-rules plugin on (tied to pnwf)" (pnOn.pnPlugin == true))
      (row "pn on, claude off: pnwf still follows pn only" (pnOnClaudeOff.pnwf == true))
      (row "pnwf vetoed with pn on: pnwf off" (pnwfVetoed.pnwf == false))
      (row "pnwf vetoed with pn on: plugin off (override tracks pnwf)" (pnwfVetoed.pnPlugin == false))
      (row "explicit pnwf.enable wins upward with pn off" (explicitPnwf.pnwf == true))
      (row "explicit pnwf.enable pulls the plugin on with pn off" (explicitPnwf.pnPlugin == true))
      (row "pkgs without pnwf: pnwf off (availability guard)" (pkgsNoPnwf.pnwf == false))
      (row "pkgs without pnwf: plugin off with it" (pkgsNoPnwf.pnPlugin == false))
    ];

    wsplan = [
      (row "pn off: wsplan off" (pnOff.wsplan == false))
      (row "pn on: wsplan on" (pnOn.wsplan == true))
      (row "pn on, claude off: wsplan on (follows pn, not claude)" (pnOnClaudeOff.wsplan == true))
      (row "pnwf vetoed: wsplan still on (follows pn, not pnwf)" (pnwfVetoed.wsplan == true))
      (row "pkgs without wsplan: wsplan off, no eval error" (wsplanOnlyAbsent.wsplan == false))
      (row "pkgs without wsplan: pnwf unaffected" (wsplanOnlyAbsent.pnwf == true))
      (row "explicit wsplan.enable wins upward with pn off" (explicitWsplan.wsplan == true))
    ];

    integrate-branch-support = [
      (row "claude on, pn off: detector on (not a pn tool)" (pnOff.ibs == true))
      (row "claude on, pn off: integrate-branch plugin on" (pnOff.ibPlugin == true))
      (row "claude off: detector off" (pnOnClaudeOff.ibs == false))
      (row "detector vetoed: plugin off (override tracks the detector)" (ibsVetoed.ibPlugin == false))
      (row "detector vetoed: pnwf/plugin unaffected" (
        ibsVetoed.pnwf == true && ibsVetoed.pnPlugin == true
      ))
      (row "explicit detector enable wins upward with claude off" (explicitIbs.ibs == true))
    ];

    ties = [
      (row "evaluation completes with no enable/override cycle" (
        builtins.deepSeq [ pnOff pnOn both ] true
      ))
      (row "capabilities.pn-workspace sets pn.enable" (viaCapability.pn == true))
      (row "capabilities.pn-workspace turns pnwf, wsplan and the plugin on" (
        viaCapability.pnwf && viaCapability.wsplan && viaCapability.pnPlugin == true
      ))
      (row "bundles.agent-support enables pn (mkDefault true)" (viaBundle.pn == true))
      (row "bundles.agent-support turns pnwf, wsplan, detector and both plugins on" (
        viaBundle.pnwf
        && viaBundle.wsplan
        && viaBundle.ibs
        && viaBundle.pnPlugin == true
        && viaBundle.ibPlugin == true
      ))
      (row "a plain pn.enable = false vetoes the bundle's mkDefault" (bundleVetoed.pn == false))
      (row "veto of pn through the bundle turns pnwf, wsplan and the plugin off" (
        !bundleVetoed.pnwf && !bundleVetoed.wsplan && bundleVetoed.pnPlugin == false
      ))
      (row "pn off alone leaves the pn capability inert" (pnOff.pn == false))
    ];
  };

  # A check derivation that fails, listing every false row, unless all rows hold.
  checkFor =
    key:
    let
      bad = map (r: r.name) (lib.filter (r: !r.ok) rows.${key});
    in
    pkgs.runCommand "pn-ties-${key}-ok" { } (
      if bad == [ ] then
        "touch $out"
      else
        "echo ${
          lib.escapeShellArg ("FAIL pn-ties ${key}:\n" + lib.concatMapStringsSep "\n" (n: "  - " + n) bad)
        } >&2; exit 1"
    );
in
{
  inherit rows checkFor;
}
