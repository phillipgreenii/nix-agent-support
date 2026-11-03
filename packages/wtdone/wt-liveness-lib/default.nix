{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "wt-liveness-lib";
  src = ./.;
  description = "Shared process-liveness allow-list: lsof cwd probe, name normalization and blocking-vs-ignorable classification for worktree teardown tools";
  inherit testSupport;
  testDeps = [
    pkgs.coreutils
    pkgs.lsof
  ];
}
