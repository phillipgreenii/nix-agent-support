{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "beads-exporter";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a self-contained module rooted
  # at this package dir, with no local `replace`, so no sibling needs to be in
  # the same store tree. The binary execs bd as a subprocess (by the absolute
  # path the configuration file names); the committed queue mirror is read only
  # by the tests, which run through the beads-exporter-go-tests check.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/beads-exporter" ];

  meta = {
    description = "Prometheus exporter publishing bead-tracker content metrics from a pinned, read-only bd";
    mainProgram = "beads-exporter";
  };
}
