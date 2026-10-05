{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "beads-exporter";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single zero-dependency module
  # rooted at this package dir, with go.mod and the committed gomod2nix.toml side
  # by side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed. The binary execs bd as a subprocess (by the absolute path the
  # configuration file names) and has no compile-time dependency on any other
  # package in this repo; the committed queue mirror is read only by the tests,
  # which run through the beads-exporter-go-tests check.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/beads-exporter" ];

  meta = {
    description = "Prometheus exporter publishing bead-tracker content metrics from a pinned, read-only bd";
    mainProgram = "beads-exporter";
  };
}
