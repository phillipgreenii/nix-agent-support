{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "ccpool-probe";

  # Pattern B (phillipg-nix-repo-base ADR 0008): go.mod replaces ../pg-connector
  # for its published client package (decodes pg-connector's answers), so the
  # build sandbox holds this module and that one at their relative positions.
  #
  # It still execs both `ccpool` and `pg-connector` as ambient-$PATH
  # subprocesses for every read/write; pg-connector is a compile-time
  # dependency only for the client package's result types [design:
  # "pg-router-probe and ccpool-probe" intro].
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-connector
    ];
  };
  modRoot = "ccpool-probe";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/ccpool-probe" ];

  meta = {
    description = "Deterministic health probe over ccpool's own operational health from pg-router's point of view, filing/updating an `escalated`-labeled bd issue via pg-connector on a real finding";
    mainProgram = "ccpool-probe";
  };
}
