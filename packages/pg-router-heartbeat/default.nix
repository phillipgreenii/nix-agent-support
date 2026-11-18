{
  lib,
  mkGoApp,
}:

let
  members = import ../pg-router/members.nix { inherit lib; };
in
mkGoApp {
  pname = "pg-router-heartbeat";

  # Pattern B (phillipg-nix-repo-base ADR 0008): this binary is built from the
  # router module (packages/pg-router, one go.mod). The fileset is exactly this
  # member's cmd directories, its internal/heartbeat subtree, the shared set and the
  # module files (members.json lists them; the Go boundary test reads the same
  # file), so a router-engine edit does not bump this binary's --version.
  src = lib.fileset.toSource {
    root = members.packagesRoot;
    fileset = members.memberFileset "heartbeat";
  };
  modRoot = "pg-router";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

  gomod2nixToml = ../pg-router/gomod2nix.toml;

  subPackages = [ "cmd/pg-router-heartbeat" ];

  meta = {
    description = "Out-of-stack dead-man switch: pages when no Prometheus sample for any job arrived for 30 awake minutes";
    mainProgram = "pg-router-heartbeat";
  };
}
