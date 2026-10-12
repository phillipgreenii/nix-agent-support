{
  lib,
  mkGoApp,
  makeWrapper,
  # gh is pkgs.gh (auto via callPackage): the daemon makes its REST calls
  # through `gh api --include` so it can read the X-RateLimit-* headers (`gh run
  # list` exposes none), so gh MUST be on this binary's own PATH. Wrapped onto
  # the binary itself, like pg-connector-pr-github (bead pg2-bygev), so a caller
  # with a narrow PATH (the launchd supervisor, pg-router) needs no gh of its own.
  gh,
  ...
}:

# Packaging for the daemon-backed GitHub connector (cmd/pg-connector-
# github). It shares the SAME Go module as ./default.nix (one go.mod, one
# gomod2nix.toml), builds from a filtered src, and is the file
# cmd/pg-connector/fileset_coverage_test.go requires beside go.mod for every
# cmd/pg-connector-* directory: the test parses the fileset below and checks
# it covers the binary's Go import closure, because mkGoApp defaults doCheck =
# false and only a real nix build otherwise sees an omitted directory.
#
# ./pkg is included whole (shared library pkg/connectord and the provider and
# scriptout surface the binary imports); the binary's own ./cmd/pg-connector-
# github tree carries its internal/ helpers (api, gitenv, github, vcs, ...), so
# later packets that add shared packages under ./pkg need no edit here. The
# overlay entry is in flake.nix; the home-manager wiring is the module's own.
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

  # wrapProgram: puts `gh` on the binary's own PATH (see the `gh` argument).
  nativeBuildInputs = [ makeWrapper ];
  postInstall = ''
    wrapProgram $out/bin/pg-connector-github --prefix PATH : ${lib.makeBinPath [ gh ]}
  '';

  meta = with lib; {
    description = "pg-connector's daemon-backed GitHub connector";
    platforms = platforms.all;
  };
}
