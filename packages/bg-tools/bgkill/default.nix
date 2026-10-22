{
  mkBashScript,
  pkgs,
  bg-tools-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "bgkill";
  src = ./.;
  description = "Stop a background job launched with bgrun, by name";
  libraries = [ bg-tools-lib ];
  runtimeDeps = [
    pkgs.coreutils
    pkgs.gawk
    pkgs.gnused
  ];
  testDeps = [
    pkgs.coreutils
    pkgs.gawk
    pkgs.gnused
  ];
  inherit testSupport;
}
