{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "git-branch-status";
  src = ./.;
  description = "Show branch status for all local branches compared to main";
  runtimeDeps = [ pkgs.git ];
  testDeps = [ pkgs.git ];
}
