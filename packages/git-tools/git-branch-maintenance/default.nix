{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "git-branch-maintenance";
  src = ./.;
  description = "Maintain git branches with fast-forward, rebase, and cleanup operations";
  runtimeDeps = [ pkgs.git ];
  testDeps = [ pkgs.git ];
}
