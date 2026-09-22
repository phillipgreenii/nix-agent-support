{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "ccpool-probe";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed (mirrors packages/pg-router-probe/default.nix, whose own
  # subprocess-exec shape this package also mirrors).
  #
  # This binary has no compile-time dependency on packages/ccpool or
  # packages/pg-connector: it execs both `ccpool` and `pg-connector` as
  # ambient-$PATH subprocesses for every read/write, rather than importing
  # or vendoring either (this repo's own CLAUDE.md forbids a dependency on
  # another custom flake within this repo too — every adapter in this repo
  # execs its backing tools, it never links against them) [design:
  # "pg-router-probe and ccpool-probe" intro].
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/ccpool-probe" ];

  meta = {
    description = "Deterministic health probe over ccpool's own operational health from pg-router's point of view, filing/updating an `escalated`-labeled bd issue via pg-connector on a real finding";
    mainProgram = "ccpool-probe";
  };
}
