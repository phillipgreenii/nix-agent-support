{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness, consumed BY
  # REFERENCE from phillipg-nix-repo-base's `git-fixture-harness` package
  # (pkgs.git-fixture-harness via this flake's overlay) -- no vendored copy.
  testSupport = "${pkgs.git-fixture-harness}/lib/scripts";

  # The single audited place the pg-rescue script roles push a rebased PR
  # branch from (docket pg2-q6a96, design D5). Its own packages/ entry, like
  # pg-rescue-flake-lock-conflict: packages/pg-rescue is a Go module and no
  # package in this repo mixes a Go module with mkBashScript.
  pg-rescue-push-branch = pkgs.callPackage ./pg-rescue-push-branch {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
in
{
  inherit pg-rescue-push-branch;
  inherit (pg-rescue-push-branch) packages tldr;
  checks = {
    test-pg-rescue-push-branch = pg-rescue-push-branch.check;
  };
}
