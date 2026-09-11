{
  mkBashScript,
  pkgs,
  session-mode-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "session-mode";
  src = ./.;
  description = "Track which named mode (drain-beads, unblock-human-beads, wrap-up-session, ...) is running in this Claude Code session";
  libraries = [ session-mode-lib ];
  runtimeDeps = [
    pkgs.coreutils
    pkgs.jq
  ];
  testDeps = [
    pkgs.coreutils
    pkgs.jq
  ];
  inherit testSupport;
}
