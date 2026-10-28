{
  pkgs,
  bashBuilders,
  # pg-nix-log-wrapped (phillipg-nix-repo-base), passed by the overlay; null when
  # the locked base flake does not carry it.
  pgNixLogWrapped ? null,
}:
let
  # pg-repo-runner (bead pg2-zj2v7m, F1 of the async-runners design): the kernel
  # and facade of the pg-router-managed repo runners. Its own packages/ entry,
  # like pg-rescue-push-branch. Its tests build real git fixtures through
  # GFH_LIB, which every package-level bats check exports, so no testSupport is
  # passed.
  pg-repo-runner = pkgs.callPackage ./pg-repo-runner {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pgNixLogWrapped;
  };
in
{
  inherit pg-repo-runner;
  inherit (pg-repo-runner) packages tldr;
  checks = {
    test-pg-repo-runner = pg-repo-runner.check;
  };
}
