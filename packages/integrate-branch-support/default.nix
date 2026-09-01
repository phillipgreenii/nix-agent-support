{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-31f13;
  # design pg2-gucfd; vendored pending pg2-ljn47.1's cross-repo nix packaging
  # -- see test-support/git-fixture-harness.bash's own header).
  testSupport = ./test-support;

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
}
