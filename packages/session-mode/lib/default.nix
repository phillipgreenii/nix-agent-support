{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "session-mode-lib";
  src = ./.;
  description = "Shared conventions for the session-mode per-session mode-tracking CLI";
  inherit testSupport;
  testDeps = [
    pkgs.coreutils
    pkgs.jq
  ];
}
