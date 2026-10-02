{
  pkgs,
  bashBuilders,
  # bd, for the test checks ONLY (the real-bd throwaway-DB fixture). At runtime
  # the script resolves `bd` from the ambient machine PATH (the machine wrapper
  # sets BEADS_DOLT_AUTO_START=0 and the claim/tracker guards), exactly like
  # every other bd-calling script in this workspace, so it is deliberately NOT
  # a runtimeDeps entry.
  beads,
}:
let
  # Shared bats helper (bd_fixture.bash): the throwaway-bd-DB isolation harness.
  testSupport = ./test-support;

  handoff-create = pkgs.callPackage ./handoff-create {
    inherit (bashBuilders) mkBashScript;
    inherit testSupport beads;
  };
in
{
  inherit handoff-create;
  inherit (handoff-create) packages tldr;
  checks = {
    test-handoff-create = handoff-create.check;
  };
}
