{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-31f13;
  # design pg2-gucfd), consumed BY REFERENCE from phillipg-nix-repo-base's
  # `git-fixture-harness` package (pkgs.git-fixture-harness via this flake's
  # overlay; pg2-xy4w7) -- no vendored copy to drift.
  testSupport = "${pkgs.git-fixture-harness}/lib/scripts";

  integrate-branch-support = pkgs.callPackage ./integrate-branch-support {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
in
{
  inherit integrate-branch-support;
  inherit (integrate-branch-support) packages tldr;
  checks = {
    test-integrate-branch-support = integrate-branch-support.check;
  };

  # Opt-in tier (INTEGRATE_BRANCH_SUPPORT_REUSE_TESTS=1): the FF-1b pass-reuse
  # tests (multi-commit fixtures with a linked worktree and a rebase; seconds per
  # test). NOT a check: the flake publishes it under
  # `legacyPackages.<system>.integration-checks` (ADR 0024 in nix-repo-base) so
  # `nix flake check` never builds it, and no hook or gate runs it (global rule
  # V-4). Build by name when the reuse decision changes:
  # `nix build --no-link .#integration-checks.test-integrate-branch-support-pre-land-reuse`.
  integration-checks = {
    test-integrate-branch-support-pre-land-reuse = integrate-branch-support.check.overrideAttrs (_: {
      INTEGRATE_BRANCH_SUPPORT_REUSE_TESTS = "1";
    });
  };
}
