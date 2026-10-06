{
  lib,
  mkGoApp,
  makeWrapper,
  pg-connector,
}:

mkGoApp {
  pname = "work-report";

  # go.mod locally replaces ../pg-connector (the test suite drives
  # pkg/scriptout's fake-backend doubles; production code never imports it),
  # so the build sandbox must contain both package dirs at their relative
  # positions (Pattern B, `phillipg-nix-repo-base` ADR 0008; mirrors
  # packages/pg-desk/default.nix).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-connector
    ];
  };
  modRoot = "work-report";

  # gomod2nix engine (ADR 0008, Case B): the toml tracks only third-party
  # deps; the local-replace module (../pg-connector) is symlinked from source
  # and is intentionally absent from it.
  gomod2nixToml = ./gomod2nix.toml;

  # This package build is NOT the Go test gate: mkGoApp defaults
  # `doCheck = false`. The whole-module gate is
  # `checks.<system>.work-report-go-tests` in flake.nix (base `mkGoTest`, same
  # Pattern-B fileset + modRoot as `src` above).

  # Exports its version as `main.Version` (cmd/work-report/main.go),
  # mkGoApp's default ldflag target, so no versionPath override is needed.

  nativeBuildInputs = [ makeWrapper ];

  # work-report execs the pg-connector binary from PATH and nothing else; the
  # wrapper supplies it at runtime, resolved by its bare $PATH name, never as a
  # compile-time import.
  postInstall = ''
    wrapProgram $out/bin/work-report --prefix PATH : ${lib.makeBinPath [ pg-connector ]}
  '';

  meta = {
    description = "work-report records and reports work activity surfaced through pg-connector";
    mainProgram = "work-report";
  };
}
