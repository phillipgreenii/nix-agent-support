{
  pkgs,
  bashBuilders,
}:
let
  pg-disk-reclaimer = pkgs.callPackage ./pg-disk-reclaimer {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pg-disk-reclaimer;
  inherit (pg-disk-reclaimer) packages tldr;
  checks = {
    # A Linux sandbox has a working ps (procps), so a sweep case that cannot run
    # there is a bug: PGDR_REQUIRE_SWEEP_TOOLS=1 makes it fail instead of skip
    # (tests/sweep-tools.bash, bead pg2-sbvin). The darwin sandbox denies ps, so
    # there the cases still skip and the `pg-disk-reclaimer-tmp-sweep-test` hook
    # (scripts/run-tmp-sweep-bats.sh) is the gate that really runs them.
    test-pg-disk-reclaimer =
      if pkgs.stdenv.hostPlatform.isLinux then
        pg-disk-reclaimer.check.overrideAttrs (_: {
          PGDR_REQUIRE_SWEEP_TOOLS = "1";
        })
      else
        pg-disk-reclaimer.check;
  };
}
