{
  pkgs,
  bashBuilders,
  # pg-nix-log-wrapped (phillipg-nix-repo-base), passed by the overlay; null when
  # the locked base flake does not carry it.
  pgNixLogWrapped ? null,
  # bd, for the test check ONLY (the real-bd throwaway-database fixture of the
  # failure-surfacing suite). At runtime the runner resolves `bd` from the
  # ambient machine PATH, like every other bd-calling script in this workspace,
  # so it is deliberately NOT a runtimeDeps entry. null leaves it out of the
  # test dependencies (that suite then skips).
  beads ? null,
}:
let
  # pg-repo-runner (bead pg2-zj2v7m, F1 of the async-runners design): the kernel
  # and facade of the pg-router-managed repo runners. Its own packages/ entry,
  # like pg-rescue-push-branch. Its tests build real git fixtures through
  # GFH_LIB, which every package-level bats check exports; the failure-surfacing
  # suite (bead pg2-loswfh, F3) also loads a per-package copy of the real-bd
  # throwaway-database fixture from ./test-support.
  pg-repo-runner = pkgs.callPackage ./pg-repo-runner {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pgNixLogWrapped beads;
    testSupport = ./test-support;
  };
in
{
  inherit pg-repo-runner;
  inherit (pg-repo-runner) packages tldr;
  checks = {
    test-pg-repo-runner = pg-repo-runner.check;
  };
}
