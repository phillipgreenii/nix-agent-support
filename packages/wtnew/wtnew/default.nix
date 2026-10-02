{
  mkBashScript,
  pkgs,
  integrate-branch-support,
  testSupport ? null,
}:
mkBashScript {
  name = "wtnew";
  src = ./.;
  description = "Create a fresh git worktree for manual (non-drain) work, linking the pre-commit config for legacy repos and printing integrate-branch-support's facts block";
  runtimeDeps = [
    pkgs.git
    pkgs.jq
    integrate-branch-support
  ];
  testDeps = [
    pkgs.git
    pkgs.jq
    integrate-branch-support
  ];
  inherit testSupport;
}
