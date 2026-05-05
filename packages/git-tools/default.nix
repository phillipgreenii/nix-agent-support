{
  pkgs,
  bashBuilders,
}:
let
  git-branch-maintenance = pkgs.callPackage ./git-branch-maintenance {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
  git-branch-status = pkgs.callPackage ./git-branch-status {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
  git-choose-branch = pkgs.callPackage ./git-choose-branch {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
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
