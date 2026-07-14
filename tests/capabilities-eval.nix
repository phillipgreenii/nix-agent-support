# Eval-level wiring test for the agent-tooling capabilities (Plan 5). Asserts the
# capability/bundle -> feature-flag wiring only (no packages forced), so it needs
# no llm-agents overlay / allowUnfree; the package build is validated downstream
# via the homelab --override-input build. Consumed by the capabilities-wiring-eval
# flake check, which supplies mkCapability/mkBundle/framework from nix-repo-base.
{
  lib,
  mkCapability,
  mkBundle,
  framework,
}:
let
  caps = import ../home/capabilities { inherit mkCapability mkBundle; };
  featureStubs =
    { lib, ... }:
    {
      options.phillipgreenii.programs = lib.mkOption {
        default = { };
        type = lib.types.attrsOf (lib.types.submodule { options.enable = lib.mkEnableOption "stub"; });
      };
    };
  eval =
    extra:
    (lib.evalModules {
      modules = [
        framework
        caps
        featureStubs
      ]
      ++ extra;
    }).config;
  p = c: c.phillipgreenii.programs;

  cCC = eval [ { phillipgreenii.capabilities.claude-code.enable = true; } ];
  cBundleHuman = eval [
    {
      phillipgreenii = {
        bundles.agent-support.enable = true;
        account.isHuman = true;
      };
    }
  ];
  cBundleAgent = eval [
    {
      phillipgreenii = {
        bundles.agent-support.enable = true;
        account.isHuman = false;
      };
    }
  ];
  cVeto = eval [
    {
      phillipgreenii = {
        bundles.agent-support.enable = true;
        capabilities.beads.enable = false;
      };
    }
  ];

  results = {
    cc_binary_on = (p cCC).claude-code.enable == true;
    cc_ceta_default_on = (p cCC).claude-extended-tool-approver.enable == true;
    cc_ccusage_stays_off = ((p cCC).ccusage.enable or false) == false;
    bundle_enables_beads = (p cBundleHuman).beads.enable == true;
    beads_human_gets_perles = (p cBundleHuman).perles.enable == true;
    beads_agent_no_perles = ((p cBundleAgent).perles.enable or false) == false;
    veto_beads_off = ((p cVeto).beads.enable or false) == false;
    veto_claude_code_still_on = (p cVeto).claude-code.enable == true;
  };
in
results // { allPass = lib.all (x: x) (lib.attrValues results); }
