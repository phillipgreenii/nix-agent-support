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
  ]
  # ps (pidstart in NAME.meta) is a darwin system binary; a Linux build needs procps.
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
  testDeps = [
    pkgs.coreutils
  ]
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
  inherit testSupport;
}
