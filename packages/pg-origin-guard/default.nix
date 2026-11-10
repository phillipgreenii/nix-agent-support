{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-origin-guard";

  # Pattern B (phillipg-nix-repo-base ADR 0008): go.mod carries a local
  # `replace => ../pg-failsig` (the shared failure-signature table and
  # credential redaction), so the fileset is rooted at the parent and both
  # modules sit side by side in one store copy.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      # This module's own docs stay out of its version digest: a doc-only
      # edit must not bump `--version` (repo CLAUDE.md "Versioning").
      (lib.fileset.difference ./. ./docs)
      ../pg-failsig
    ];
  };
  modRoot = "pg-origin-guard";

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-origin-guard" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # unit gate is checks.<system>.pg-origin-guard-go-tests and the integration
  # tier is checks.<system>.pg-origin-guard-integration-tests, both in flake.nix.

  # Deliberately NO wrapProgram / PATH prefix: the binary runs the ProxyCommand
  # it is given and reads the ssh agent over a socket, and must not change the
  # PATH of the wrapped command.

  # The tldr page, flat under share/tldr/pages.common (the layout pg-probe and
  # pg-keep-awake ship; the home module registers it through
  # programs.tldr.customPages).
  postInstall = ''
    mkdir -p $out/share/tldr/pages.common
    cp ${./share/tldr/pages.common/pg-origin-guard.md} $out/share/tldr/pages.common/pg-origin-guard.md
  '';

  meta = {
    description = "pg-origin-guard: keeps unattended git-auth retries from opening login windows and reports whether auth to a realm is available";
    mainProgram = "pg-origin-guard";
  };
}
