{
  lib,
  mkGoApp,
  makeWrapper,
  sqlite,
}:

mkGoApp {
  pname = "pg-desk-shadow";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset.
  #
  # The binary has no compile-time dependency on packages/pg-desk or
  # packages/pg-connector: it execs the tools it finds on PATH at `prepare`
  # time (pinning their unwrapped binaries into the scratch directory) and
  # reads the scratch store through the sqlite3 CLI, so the module stays
  # stdlib plus yaml.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-desk-shadow" ];

  nativeBuildInputs = [ makeWrapper ];

  # sqlite3 for the parent collector's store reads (the sandboxed children get
  # their own pinned PATH, never this one).
  postInstall = ''
    wrapProgram $out/bin/pg-desk-shadow --prefix PATH : ${lib.makeBinPath [ sqlite ]}
  '';

  meta = {
    description = "One-off shadow comparison of pg-desk's fingerprint change detection against the live change flow: sandboxed collector, live-log reader and report generator";
    mainProgram = "pg-desk-shadow";
  };
}
