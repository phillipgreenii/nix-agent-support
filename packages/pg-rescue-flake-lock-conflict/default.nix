{
  pkgs,
  bashBuilders,
}:
let
  # A single deterministic pg-rescue handler (bead pg2-3ybxg). It lives in its
  # own packages/ entry rather than inside packages/pg-rescue: that directory
  # is a Pattern A Go module whose derivation is `mkGoApp` over
  # `lib.cleanSource ./.`, and no package in this repo mixes a Go module with
  # mkBashScript. A separate entry keeps both builds simple; the cost is that
  # the pg-rescue-go-tests check puts this handler on PATH via its testDeps
  # (flake.nix) so the wrapper-driven tests can find it.
  pg-rescue-flake-lock-conflict = pkgs.callPackage ./pg-rescue-flake-lock-conflict {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pg-rescue-flake-lock-conflict;
  inherit (pg-rescue-flake-lock-conflict) packages tldr;
  checks = {
    test-pg-rescue-flake-lock-conflict = pg-rescue-flake-lock-conflict.check;
  };
}
