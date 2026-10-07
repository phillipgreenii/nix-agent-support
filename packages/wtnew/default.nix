{
  pkgs,
  bashBuilders,
  integrate-branch-support,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-j8fm8;
  # design pg2-gucfd), consumed BY REFERENCE from phillipg-nix-repo-base's
  # `git-fixture-harness` package (pkgs.git-fixture-harness via this flake's
  # overlay; pg2-xy4w7) -- no vendored copy to drift.
  testSupport = "${pkgs.git-fixture-harness}/lib/scripts";

  wtnew = pkgs.callPackage ./wtnew {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs integrate-branch-support testSupport;
  };
in
{
  inherit wtnew;
  inherit (wtnew) packages tldr;
  checks = {
    test-wtnew = wtnew.check;
  };
}
