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
}
