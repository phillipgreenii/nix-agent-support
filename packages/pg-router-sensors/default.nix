{
  lib,
  mkGoApp,
}:

let
  members = import ../pg-router/members.nix { inherit lib; };
in
mkGoApp {
  pname = "pg-router-sensors";

  # Pattern B (phillipg-nix-repo-base ADR 0008): this binary is built from the
  # router module (packages/pg-router, one go.mod). The fileset is exactly this
  # member's cmd directories, its internal/sensors subtree, the shared set and the
  # module files (members.json lists them; the Go boundary test reads the same
  # file), so a router-engine edit does not bump this binary's --version.
  src = lib.fileset.toSource {
    root = members.packagesRoot;
    fileset = members.memberFileset "sensors";
  };
  modRoot = "pg-router";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

  gomod2nixToml = ../pg-router/gomod2nix.toml;

  # main.go declares `var version` in lowercase; mkGoApp's default ldflag
  # target is main.Version.
  versionPath = "main.version";

  subPackages = [ "cmd/pg-router-sensors" ];

  meta = {
    description = "The router's sensors: one binary whose subcommands each print JSONL level answers (host, vpn, git-auth, beads, claude, aws-sso)";
    mainProgram = "pg-router-sensors";
  };
}
