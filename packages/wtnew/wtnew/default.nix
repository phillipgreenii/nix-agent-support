{
  mkBashScript,
  pkgs,
  integrate-branch-support,
  testSupport ? null,
}:
mkBashScript {
  name = "wtnew";
  src = ./.;
  description = "Create a fresh git worktree for manual (non-drain) work, reporting the hook-bundle state and printing integrate-branch-support's facts block";
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
