{
  mkBashScript,
  pkgs,
  bg-tools-lib,
  bgrun,
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
  ]
  # ps is a darwin system binary; a Linux build needs procps.
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
  # The suite launches real jobs with the PACKAGED bgrun (never a mock); in the
  # nix check it is on PATH, and a local run falls back to the source tree's copy.
  testDeps = [
    pkgs.coreutils
    pkgs.gawk
    pkgs.gnused
  ]
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps
  ++ bgrun.packages;
  inherit testSupport;
}
