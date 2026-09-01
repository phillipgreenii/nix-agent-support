{
  mkBashScript,
  pkgs,
  integrate-branch-support,
}:
mkBashScript {
  name = "wtnew";
  src = ./.;
  description = "Create a fresh git worktree for manual (non-drain) work, with the pre-commit symlink guaranteed and integrate-branch-support's facts block printed";
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
}
