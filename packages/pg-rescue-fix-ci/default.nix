{
  pkgs,
  bashBuilders,
  # bd, for the test check ONLY (the real-bd throwaway-DB fixture). At runtime
  # the shim resolves `bd` from the ambient machine PATH (the machine wrapper
  # sets BEADS_DOLT_AUTO_START=0 and the claim/tracker guards), exactly like
  # every other bd-calling script in this workspace, so it is deliberately NOT
  # a runtimeDeps entry.
  beads,
}:
let
  # Per-module copy of the real-bd throwaway-database fixture (bd_fixture.bash).
  testSupport = ./test-support;

  # The argv of the pg-router fix-ci-script command role (docket pg2-q6a96,
  # design D6 and D9). Its own packages/ entry, like pg-rescue-resolve-conflict:
  # packages/pg-rescue is a Go module and no package in this repo mixes a Go
  # module with mkBashScript.
  pg-rescue-fix-ci = pkgs.callPackage ./pg-rescue-fix-ci {
    inherit (bashBuilders) mkBashScript;
    inherit testSupport beads;
  };
in
{
  inherit pg-rescue-fix-ci;
  inherit (pg-rescue-fix-ci) packages tldr;
  checks = {
    test-pg-rescue-fix-ci = pg-rescue-fix-ci.check;
  };
}
