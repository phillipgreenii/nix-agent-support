{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-rescue-push-branch";
  src = ./.;
  description = "Push a rebased PR branch from a linked worktree with a pinned --force-with-lease, refusing anything else";
  # The helper only ever calls git. It runs under launchd / pg-router with a
  # thin PATH, so git and the tools around it are declared, not assumed.
  runtimeDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.gnugrep
  ];
  testDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.gnugrep
  ];
  inherit testSupport;
}
