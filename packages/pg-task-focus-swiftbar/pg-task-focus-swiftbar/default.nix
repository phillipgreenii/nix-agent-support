{
  mkBashScript,
  pkgs,
}:
# Internal renderer (public = false): invoked only by the generated SwiftBar
# plugin wrapper (../plugin.nix), never from a user's PATH, so there are no
# completions and no tldr page. `--help` still works for the default man page.
mkBashScript {
  name = "pg-task-focus-swiftbar";
  src = ./.;
  description = "SwiftBar streaming menu bar renderer for pg-task-focus: the running cycle, overtime, paused cycles, the resume offer, ended periods and the next due task";
  public = false;
  runtimeDeps = [
    pkgs.jq
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.jq
    pkgs.coreutils
  ];
}
