{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-review-router-adapter";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single stdlib-only module
  # rooted at this package dir. It runs the `pg-review` CLI (found on PATH) as a
  # child process and has no compile-time dependency on packages/pg-review, so
  # no local `replace`, modRoot or parent-rooted fileset is needed.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-review-router-adapter" ];

  # The whole-module Go test gate is checks.<system>.pg-review-router-adapter-go-tests.

  meta = {
    description = "Adapter between a pg-router command role and pg-review: turns a router dispatch into a plain pg-review job command line and maps exit codes to router semantics";
    mainProgram = "pg-review-router-adapter";
  };
}
