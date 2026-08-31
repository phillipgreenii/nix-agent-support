{
  mkBashScript,
  pkgs,
  bg-tools-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "bgcheck";
  src = ./.;
  description = "Report a background job's status and recent output in one call";
  libraries = [ bg-tools-lib ];
  runtimeDeps = [
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.coreutils
  ];
  inherit testSupport;
}
