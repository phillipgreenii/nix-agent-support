{
  pkgs,
  bashBuilders,
  # bd and hand-to-human, for the test checks ONLY (the real-bd throwaway-DB
  # fixture and the real hand-off tool). At runtime the script resolves both from
  # the ambient machine PATH (the machine wrapper for bd sets
  # BEADS_DOLT_AUTO_START=0 and the tracker guards), exactly like hand-to-human
  # itself, so neither is a runtimeDeps entry.
  beads,
  hand-to-human,
}:
let
  # Shared bats helper: hand-to-human's real-bd throwaway-database fixture, used
  # by reference (a copy would drift).
  testSupport = ../hand-to-human/test-support;

  escalation-watchdog = pkgs.callPackage ./escalation-watchdog {
    inherit (bashBuilders) mkBashScript;
    inherit testSupport beads hand-to-human;
  };
in
{
  inherit escalation-watchdog;
  inherit (escalation-watchdog) packages tldr;
  checks = {
    test-escalation-watchdog = escalation-watchdog.check;
  };
}
