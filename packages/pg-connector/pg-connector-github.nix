{
  lib,
  mkGoApp,
  ...
}:

# Minimal packaging for the daemon-backed GitHub connector (cmd/pg-connector-
# github). It shares the SAME Go module as ./default.nix (one go.mod, one
# gomod2nix.toml), builds from a filtered src, and is the file
# cmd/pg-connector/fileset_coverage_test.go requires beside go.mod for every
# cmd/pg-connector-* directory: the test parses the fileset below and checks
# it covers the binary's Go import closure, because mkGoApp defaults doCheck =
# false and only a real nix build otherwise sees an omitted directory.
#
# ./pkg is included whole (shared library pkg/connectord and the provider and
# scriptout surface the binary imports); the binary's own ./cmd/pg-connector-
# github tree carries its internal/ helpers. The gh wrapper (wrapProgram), the
# overlay entry and the home-manager wiring belong to the package's own
# packaging work, not to this skeleton.
mkGoApp {
  pname = "pg-connector-github";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg
      ./cmd/pg-connector-github
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-github" ];

  # Exports its version as `main.Version`, like every pg-connector backend.
  versionPath = "main.Version";

  meta = with lib; {
    description = "pg-connector's daemon-backed GitHub connector";
    platforms = platforms.all;
  };
}
