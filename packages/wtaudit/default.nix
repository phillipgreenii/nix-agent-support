{
  pkgs,
  bashBuilders,
  # wtdone, pinned into the script through config.WTDONE_BIN (the one tool whose
  # identity matters; see wtaudit/default.nix).
  wtdone,
  # bd, for the test checks ONLY (the real-bd throwaway-DB fixture). At runtime
  # the script resolves `bd` (and `pn`, `pnwf`) from the ambient machine PATH,
  # exactly like handoff-create and hand-to-human, so none is a runtimeDeps entry.
  beads,
}:
let
  # Shared bats helper (bd_fixture.bash): the throwaway-bd-DB isolation harness,
  # a per-module copy as hand-to-human and handoff-create keep. The git fixture
  # comes from the harness through GFH_LIB, not through testSupport.
  testSupport = ./test-support;

  inherit (import ../wtdone { inherit pkgs bashBuilders; }) wt-liveness-lib;

  wtaudit = pkgs.callPackage ./wtaudit {
    inherit (bashBuilders) mkBashScript;
    inherit
      testSupport
      beads
      wtdone
      wt-liveness-lib
      ;
  };
in
{
  inherit wtaudit;
  inherit (wtaudit) packages tldr;
  checks = {
    test-wtaudit = wtaudit.check;
  };
}
