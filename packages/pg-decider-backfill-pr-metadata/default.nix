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

  # The second one-off of the minted-by stamp (the per-PR work-item lookup): labels the
  # decider's existing beads. Same fixture and wiring, so it lives here.
  mintedBy = pkgs.callPackage ./pg-decider-backfill-minted-by {
    inherit (bashBuilders) mkBashScript;
    inherit testSupport beads;
  };
in
{
  pg-decider-backfill-pr-metadata = backfill;
  pg-decider-backfill-minted-by = mintedBy;
  packages = backfill.packages ++ mintedBy.packages;
  tldr = backfill.tldr // mintedBy.tldr;
  checks = {
    test-pg-decider-backfill-pr-metadata = backfill.check;
    test-pg-decider-backfill-minted-by = mintedBy.check;
  };
}
