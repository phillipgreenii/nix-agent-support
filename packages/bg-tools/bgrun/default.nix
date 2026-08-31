{
  mkBashScript,
  pkgs,
  bg-tools-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "bgrun";
  src = ./.;
  description = "Launch a command in the background with a truthful exit record";
  libraries = [ bg-tools-lib ];
  runtimeDeps = [
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.coreutils
  ];
  inherit testSupport;
}
