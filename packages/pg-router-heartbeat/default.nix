{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-heartbeat";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single stdlib-only module
  # rooted at this package dir. It execs the `pg-router` CLI (`gate set|clear|
  # list`, path given by --pg-router-path) as a subprocess and has no
  # compile-time dependency on packages/pg-router, so no local `replace`,
  # modRoot or parent-rooted fileset is needed (same shape as
  # packages/pg-router-probe).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-router-heartbeat" ];

  meta = {
    description = "Out-of-stack dead-man switch: pages when no Prometheus sample for any job arrived for 30 awake minutes";
    mainProgram = "pg-router-heartbeat";
  };
}
