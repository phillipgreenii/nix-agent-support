{
  pkgs,
  bashBuilders,
  integrate-branch-support,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-j8fm8;
  # design pg2-gucfd; vendored pending pg2-ljn47.1's cross-repo nix packaging
  # -- see test-support/git-fixture-harness.bash's own header).
  testSupport = ./test-support;

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
