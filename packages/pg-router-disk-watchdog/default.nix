{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-disk-watchdog";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single stdlib-only module
  # rooted at this package dir. It execs the `pg-router` CLI (`gate set|clear|
  # list`, path given by --pg-router-path) as a subprocess and has no
  # compile-time dependency on packages/pg-router, so no local `replace`,
  # modRoot or parent-rooted fileset is needed (same shape as
  # packages/pg-router-probe).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-router-disk-watchdog" ];

  meta = {
    description = "External disk-space watchdog for pg-router: sets/clears the LOW_DISK_USAGE gate through the pg-router gate CLI when free space crosses a configurable threshold";
    mainProgram = "pg-router-disk-watchdog";
  };
}
