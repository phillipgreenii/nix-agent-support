{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "prpool-ccpool-handler";

  # Pattern B (local `replace => ../pg-router`, `phillipg-nix-repo-base` ADR
  # 0008): root the fileset at the parent so pg-router's source sits alongside
  # this module in one store copy, mirroring pg-router's own local-replace of
  # ../ccpool and ../claude-transcript (packages/pg-router/default.nix). This
  # module is currently an empty, buildable shell — see doc.go and
  # docs/behavior/README.md's Realization gaps; no participant code has
  # moved in yet (Task 5.2 onward).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      # Exclude this module's OWN behavior docs from its version digest, same
      # rationale as pg-router's own default.nix: a doc-only edit here should
      # not bump this package's `--version` (repo CLAUDE.md "Versioning";
      # ADR 0025).
      (lib.fileset.difference ./. ./docs)
      ../pg-router
    ];
  };
  modRoot = "prpool-ccpool-handler";

  gomod2nixToml = ./gomod2nix.toml;

  meta = {
    description = "pg-router participant implementations (ccpool-backed and command-backed handlers) — scaffold, no participant code yet";
  };
}
