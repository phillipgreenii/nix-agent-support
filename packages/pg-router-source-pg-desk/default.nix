{
  lib,
  mkGoApp,
  makeWrapper,
  pg-desk,
}:

let
  members = import ../pg-router/members.nix { inherit lib; };
in
mkGoApp {
  pname = "pg-router-source-pg-desk";

  # Pattern B (phillipg-nix-repo-base ADR 0008): this binary is built from the
  # router module (packages/pg-router, one go.mod). The fileset is exactly this
  # member's cmd directory, its internal/source/desk subtree, the shared set and
  # the module files (members.json lists them; the Go boundary test reads the
  # same file), so a router-engine edit does not bump this binary's --version.
  #
  # This binary has no compile-time dependency on packages/pg-desk: it execs
  # pg-desk as a subprocess and parses its stdout JSON generically.
  src = lib.fileset.toSource {
    root = members.packagesRoot;
    fileset = members.memberFileset "source-desk";
  };
  modRoot = "pg-router";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

  gomod2nixToml = ../pg-router/gomod2nix.toml;

  subPackages = [ "cmd/pg-router-source-pg-desk" ];

  nativeBuildInputs = [ makeWrapper ];

  # Supplies pg-desk (and, through pg-desk's own wrapper, pg-connector) on
  # PATH, resolved at runtime by its bare name — mirrors packages/pg-desk's
  # own wrapProgram call for pg-connector. The unwrapped binary is left behind
  # as `.pg-router-source-pg-desk-wrapped`, which the live-exercise negative
  # control runs under `env -i`.
  postInstall = ''
    wrapProgram $out/bin/pg-router-source-pg-desk --prefix PATH : ${lib.makeBinPath [ pg-desk ]}
  '';

  meta = {
    description = "Adapter binary exposing pg-desk's changes envelope as pg-router command-query items";
    mainProgram = "pg-router-source-pg-desk";
  };
}
