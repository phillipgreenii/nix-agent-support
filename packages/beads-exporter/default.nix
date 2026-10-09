{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "beads-exporter";

  # Pattern B (phillipg-nix-repo-base ADR 0008): the module has a local
  # `replace ../claude-transcript` (the stranded-claim pass discovers session
  # transcripts through it), so the build sandbox must contain BOTH package dirs
  # at their relative positions. Root the source at packages/ and build the
  # beads-exporter subdir. The binary execs bd as a subprocess (by the absolute
  # path the configuration file names); the committed queue mirror is read only
  # by the tests, which run through the beads-exporter-go-tests check.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../claude-transcript
    ];
  };
  modRoot = "beads-exporter";

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/beads-exporter" ];

  meta = {
    description = "Prometheus exporter publishing bead-tracker content metrics from a pinned, read-only bd";
    mainProgram = "beads-exporter";
  };
}
