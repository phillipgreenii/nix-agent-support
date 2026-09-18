{
  pkgs,
  bashBuilders,
}:
let
  # pgWiFlowLib: the pg-wi-flow CLI's shared bash libraries -- actor
  # composition (bead tc-q25wo item 2, lib/actor.bash), the two-layer
  # config loader (lib/config.bash, tc-9ddu3.1.1), and the bd Adapter
  # (lib/tracker.bash, tc-9ddu3.1.1, the ONLY file that invokes `bd`).
  # mkBashLibrary wraps exactly one named source file per call, so this
  # directory produces three sibling library derivations rather than one.
  pgWiFlowLib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs;
  };

  # pg-wi-flow-identity: the ceta input processor (bead tc-q25wo item 1).
  pg-wi-flow-identity = pkgs.callPackage ./pg-wi-flow-identity {
    inherit (bashBuilders) mkBashScript;
  };

  # pg-wi-flow: the CLI itself. tc-9ddu3.1.1 adds query/list/next/claim/
  # release; later packets in the tc-9ddu3.1 docket add more subcommands to
  # this same script.
  pg-wi-flow = pkgs.callPackage ./pg-wi-flow {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pgWiFlowLib;
  };
in
{
  inherit pgWiFlowLib pg-wi-flow-identity pg-wi-flow;
  packages = pg-wi-flow-identity.packages ++ pg-wi-flow.packages;
  tldr = pg-wi-flow-identity.tldr // pg-wi-flow.tldr;
  checks = {
    test-pg-wi-flow-actor = pgWiFlowLib.actor.check;
    test-pg-wi-flow-config = pgWiFlowLib.config.check;
    test-pg-wi-flow-tracker = pgWiFlowLib.tracker.check;
    test-pg-wi-flow-identity = pg-wi-flow-identity.check;
    test-pg-wi-flow-cli = pg-wi-flow.check;
  };
}
