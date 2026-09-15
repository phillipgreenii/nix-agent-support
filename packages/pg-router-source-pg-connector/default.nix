{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-source-pg-connector";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed (mirrors packages/pg-ccaudit/default.nix).
  #
  # This binary has no compile-time dependency on packages/pg-connector: it
  # execs pg-connector as a subprocess and parses its stdout JSON generically
  # [design: section 6.1]. Reusing packages/pg-connector's own pkg/schema
  # types was therefore never an option here — there is nothing to reuse
  # across that seam by design.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-router-source-pg-connector" ];

  meta = {
    description = "Adapter binary exposing pg-connector's changes/list verbs as pg-router command-query rawItem sources (changes/sweep/list)";
    mainProgram = "pg-router-source-pg-connector";
  };
}
