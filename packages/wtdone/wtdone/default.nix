{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:
mkBashScript {
  name = "wtdone";
  src = ./.;
  description = "Guarded worktree teardown -- lsof liveness guard, fsmonitor stop, worktree remove, plain branch -d, prune";
  runtimeDeps = [
    pkgs.git
    pkgs.lsof
  ];
  testDeps = [
    pkgs.git
    pkgs.lsof
  ];
  inherit testSupport;
}
