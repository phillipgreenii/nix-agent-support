{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-31f13; design
  # pg2-gucfd; vendored pending pg2-ljn47.1's cross-repo nix packaging -- see
  # test-support/git-fixture-harness.bash's own header). One copy, shared by
  # all three sub-tools' bats suites, per pg2-gucfd's shared-harness decision.
  testSupport = ./test-support;

  git-branch-maintenance = pkgs.callPackage ./git-branch-maintenance {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
  git-branch-status = pkgs.callPackage ./git-branch-status {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
  git-choose-branch = pkgs.callPackage ./git-choose-branch {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };
in
{
  inherit git-branch-maintenance git-branch-status git-choose-branch;
  packages =
    git-branch-maintenance.packages ++ git-branch-status.packages ++ git-choose-branch.packages;
  tldr = git-branch-maintenance.tldr ++ git-branch-status.tldr ++ git-choose-branch.tldr;
  checks = {
    test-git-branch-maintenance = git-branch-maintenance.check;
    test-git-branch-status = git-branch-status.check;
    test-git-choose-branch = git-choose-branch.check;
  };
}
