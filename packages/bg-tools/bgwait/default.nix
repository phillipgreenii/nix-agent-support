{
  mkBashScript,
  pkgs,
  bg-tools-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "bgwait";
  src = ./.;
  description = "Block until a background job finishes, then report it once";
  libraries = [ bg-tools-lib ];
  runtimeDeps = [
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.coreutils
  ];
  inherit testSupport;
}
