{
  lib,
  mkGoApp,
  makeWrapper,
  # Resolves automatically via callPackage against `final.pg-review`.
  pg-review,
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

  # The adapter execs `pg-review` by bare name in its own process, which
  # inherits the pg-router daemon's environment. That environment does not
  # carry the per-user profile on PATH (strict base env) and pg-review is
  # installed only through that profile, so the adapter wraps its own runtime
  # dependency. `--prefix` keeps the inherited PATH. Guarded by the
  # `test-pg-review-router-adapter-resolves-pg-review` flake check.
  nativeBuildInputs = [ makeWrapper ];
  postInstall = ''
    wrapProgram $out/bin/pg-review-router-adapter --prefix PATH : ${lib.makeBinPath [ pg-review ]}
  '';

  meta = {
    description = "Adapter between a pg-router command role and pg-review: turns a router dispatch into a plain pg-review job command line and maps exit codes to router semantics";
    mainProgram = "pg-review-router-adapter";
  };
}
