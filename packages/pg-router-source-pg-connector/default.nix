{
  lib,
  mkGoApp,
}:

let
  members = import ../pg-router/members.nix { inherit lib; };
in
mkGoApp {
  pname = "pg-router-source-pg-connector";

  # Pattern B (phillipg-nix-repo-base ADR 0008): this binary is built from the
  # router module (packages/pg-router, one go.mod). The fileset is exactly this
  # member's cmd directory, its internal/source/connector subtree, the shared set
  # and the module files (members.json lists them; the Go boundary test reads the
  # same file), so a router-engine edit does not bump this binary's --version.
  #
  # It still execs pg-connector as a subprocess (ambient $PATH binary), but
  # decodes the answers with the published client package of
  # packages/pg-connector (a local replace of ../pg-connector, carried by
  # members.memberFileset).
  src = lib.fileset.toSource {
    root = members.packagesRoot;
    fileset = members.memberFileset "source-connector";
  };
  modRoot = "pg-router";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

  gomod2nixToml = ../pg-router/gomod2nix.toml;

  subPackages = [ "cmd/pg-router-source-pg-connector" ];

  meta = {
    description = "Adapter binary exposing pg-connector's changes/list verbs as pg-router command-query rawItem sources (changes/sweep/list)";
    mainProgram = "pg-router-source-pg-connector";
  };
}
