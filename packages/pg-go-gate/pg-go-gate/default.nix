{
  mkBashScript,
  pkgs,
}:
mkBashScript {
  name = "pg-go-gate";
  src = ./.;
  description = "Host-wide slot gate for agent go test/build/install, run under nice 10";
  runtimeDeps = [
    pkgs.coreutils
  ]
  # ps is a darwin system binary; a Linux build needs procps.
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
  testDeps = [
    pkgs.coreutils
  ]
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
}
