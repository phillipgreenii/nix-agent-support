{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-hpurf;
  # design pg2-gucfd; vendored pending pg2-ljn47.1's cross-repo nix packaging
  # -- see test-support/git-fixture-harness.bash's own header).
  testSupport = ./test-support;

  wtdone = pkgs.callPackage ./wtdone {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
in
{
  inherit wtdone;
  inherit (wtdone) packages tldr;
  checks = {
    test-wtdone = wtdone.check;
  };
}
