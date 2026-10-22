{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "bg-tools-lib";
  src = ./.;
  description = "Shared state conventions for the bgrun/bgcheck/bgwait background-job helpers";
  inherit testSupport;
  testDeps = [
    pkgs.coreutils
    pkgs.gawk
    pkgs.gnused
  ]
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
}
