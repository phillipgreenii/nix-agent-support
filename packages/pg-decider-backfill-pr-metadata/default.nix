{
  pkgs,
  bashBuilders,
  # bd, for the test checks ONLY (the real-bd throwaway-DB fixture). At runtime
  # the script resolves `bd` from the ambient machine PATH (the machine wrapper
  # sets BEADS_DOLT_AUTO_START=0 and the claim/tracker guards), exactly like
  # handoff-create, so it is deliberately NOT a runtimeDeps entry.
  beads,
}:
let
  testSupport = ./test-support;

  backfill = pkgs.callPackage ./pg-decider-backfill-pr-metadata {
    inherit (bashBuilders) mkBashScript;
    inherit testSupport beads;
  };
in
{
  pg-decider-backfill-pr-metadata = backfill;
  inherit (backfill) packages tldr;
  checks = {
    test-pg-decider-backfill-pr-metadata = backfill.check;
  };
}
