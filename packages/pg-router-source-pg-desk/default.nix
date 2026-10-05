{
  lib,
  mkGoApp,
  makeWrapper,
  pg-desk,
}:

mkGoApp {
  pname = "pg-router-source-pg-desk";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed (mirrors packages/pg-router-source-pg-connector/default.nix).
  #
  # This binary has no compile-time dependency on packages/pg-desk: it execs
  # pg-desk as a subprocess and parses its stdout JSON generically.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

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
