{
  pkgs,
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-ccpool-handler";

  # Pattern B (local `replace => ../pg-router`/`../ccpool`/`../claude-transcript`,
  # `phillipg-nix-repo-base` ADR 0008): root the fileset at the parent so all
  # three sit alongside this module in one store copy, mirroring pg-router's
  # own local-replace of the same two (packages/pg-router/default.nix) —
  # `../ccpool`'s own sessionmeta subpackage is this module's test-only
  # dependency (internal/ccpool/sessionmeta_smoke_test.go), and
  # `../claude-transcript` backs internal/usage's transcript reader plus
  # ccpoolRun's own needs_input detection. Task 5.2/5.3 (folded per the
  # operator's 2026-09-11 decision) moved every participant implementation in
  # and gave the module a real wire-facing CLI entrypoint — see doc.go and
  # docs/behavior/README.md's Realization gaps.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      # Exclude this module's OWN behavior docs from its version digest, same
      # rationale as pg-router's own default.nix: a doc-only edit here should
      # not bump this package's `--version` (repo CLAUDE.md "Versioning";
      # ADR 0025).
      (lib.fileset.difference ./. ./docs)
      ../pg-router
      ../ccpool
      ../claude-transcript
    ];
  };
  modRoot = "pg-router-ccpool-handler";

  gomod2nixToml = ./gomod2nix.toml;

  # git on PATH for the moved ccpool/watchdog/beads packages' real-git
  # fixture tests (mirrors pg-router's own default.nix nativeCheckInputs).
  nativeCheckInputs = [ pkgs.git ];

  meta = {
    description = "pg-router participant implementations (ccpool-backed and command-backed handlers), speaking INTF-HANDLER/INTF-SOURCE over the wire";
  };
}
