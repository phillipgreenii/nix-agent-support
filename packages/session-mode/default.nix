{
  pkgs,
  bashBuilders,
}:
let
  testSupport = ./test-support;

  session-mode-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  session-mode = pkgs.callPackage ./session-mode {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs session-mode-lib testSupport;
  };
in
{
  inherit session-mode-lib session-mode;
  inherit (session-mode) packages tldr;
  checks = {
    test-session-mode-lib = session-mode-lib.check;
    test-session-mode = session-mode.check;
  };
}
