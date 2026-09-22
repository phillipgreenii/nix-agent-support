{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-probe";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed (mirrors packages/pg-router-source-pg-connector/default.nix,
  # which this package's own subprocess-exec shape also mirrors).
  #
  # This binary has no compile-time dependency on packages/pg-connector or
  # on Grafana client code elsewhere in this workspace: it execs
  # pg-connector as a subprocess (ambient $PATH) for every bd write/read,
  # and implements its own small Grafana HTTP client rather than importing
  # or vendoring the phillipg-nix-ziprecruiter repo's lat-survey (this repo's
  # own CLAUDE.md forbids a dependency on another custom flake) [design:
  # "pg-router-probe run checks" item 1].
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-router-probe" ];

  meta = {
    description = "Deterministic health probe over pg-router's own operational health, filing/updating an `escalated`-labeled bd issue via pg-connector on a real finding";
    mainProgram = "pg-router-probe";
  };
}
