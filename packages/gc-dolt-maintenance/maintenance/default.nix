{
  mkBashScript,
  pkgs,
  gc-otlp-emit,
  gc-dolt-maintenance-lib,
  gascity,
}:
mkBashScript {
  name = "gc-dolt-maintenance";
  src = ./.;
  description = "Hourly self-gating Dolt maintenance: stats-purge/GC always, flatten when worthwhile+safe";
  libraries = [
    gc-otlp-emit
    gc-dolt-maintenance-lib
  ];
  runtimeDeps = [
    pkgs.dolt
    gascity
    pkgs.curl
    pkgs.jq
    pkgs.coreutils
    pkgs.procps
  ];
  testDeps = [
    pkgs.dolt
    pkgs.curl
    pkgs.jq
    pkgs.coreutils
    pkgs.procps
  ];
}
