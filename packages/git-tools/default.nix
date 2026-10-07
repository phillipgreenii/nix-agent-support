{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-31f13; design
  # pg2-gucfd), consumed BY REFERENCE from phillipg-nix-repo-base's
  # `git-fixture-harness` package (surfaced as pkgs.git-fixture-harness by this
  # flake's overlay; pg2-xy4w7) -- there is no vendored copy to drift. One
  # harness, shared by all three sub-tools' bats suites, per pg2-gucfd's
  # shared-harness decision.
  testSupport = "${pkgs.git-fixture-harness}/lib/scripts";

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
