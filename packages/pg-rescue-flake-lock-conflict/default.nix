{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-emjgm),
  # consumed BY REFERENCE from phillipg-nix-repo-base's `git-fixture-harness`
  # package (pkgs.git-fixture-harness via this flake's overlay; pg2-xy4w7) --
  # no vendored copy to drift.
  testSupport = "${pkgs.git-fixture-harness}/lib/scripts";

  # A single deterministic pg-rescue handler (bead pg2-3ybxg). It lives in its
  # own packages/ entry rather than inside packages/pg-rescue: that directory
  # is a Pattern A Go module whose derivation is `mkGoApp` over
  # `lib.cleanSource ./.`, and no package in this repo mixes a Go module with
  # mkBashScript. A separate entry keeps both builds simple; the cost is that
  # the pg-rescue-go-tests check puts this handler on PATH via its testDeps
  # (flake.nix) so the wrapper-driven tests can find it.
  pg-rescue-flake-lock-conflict = pkgs.callPackage ./pg-rescue-flake-lock-conflict {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
in
{
  inherit pg-rescue-flake-lock-conflict;
  inherit (pg-rescue-flake-lock-conflict) packages tldr;
  checks = {
    test-pg-rescue-flake-lock-conflict = pg-rescue-flake-lock-conflict.check;
  };
}
