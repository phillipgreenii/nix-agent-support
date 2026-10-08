{
  mkBashScript,
  pkgs,
}:
# Internal renderer (public = false): invoked only by the generated SwiftBar
# plugin wrapper (../plugin.nix), never from a user's PATH, so there are no
# completions and no tldr page. `--help` still works for the default man page.
mkBashScript {
  name = "pa-monitor-swiftbar";
  src = ./.;
  description = "SwiftBar menu bar renderer for pa-monitor: 5h usage window, usage-limit countdown, caffeinate and auto-resume toggles";
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
