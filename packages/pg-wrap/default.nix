{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-wrap";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single standalone module
  # rooted at this package dir, go.mod and the committed gomod2nix.toml side by
  # side. pg-wrap has no local `replace`: it is mutually unaware of pg-router
  # and ccpool (design ruling 30), so no sibling package dir joins the source.
  # ./docs holds the behavior docs, not build inputs; excluding them keeps a
  # doc-only edit from changing the per-source digest (repo CLAUDE.md
  # "Versioning"; ADR 0025), as packages/pg-router/default.nix does.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.difference ./. ./docs;
  };
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # Two binaries: pg-wrap (run / check / show) and pg-wrap-exec (the
  # claude.bin-shaped entrypoint that reads PG_WRAP_SPEC and chain-loads the
  # spec's wrap chain; exit 97 when PG_WRAP_SPEC is unset).
  subPackages = [
    "cmd/pg-wrap"
    "cmd/pg-wrap-exec"
  ];

  # Not the Go test gate (mkGoApp defaults doCheck = false, bead pg2-pla9d.2):
  # the whole-module gate is checks.<system>.pg-wrap-go-tests in flake.nix.
  #
  # Deliberately NO wrapProgram / PATH prefix: pg-wrap applies the chain of
  # command prefixes a spec names, and must not carry any other tool onto the
  # PATH of the payload it wraps.

  meta = {
    description = "pg-wrap: apply an ordered chain of command prefixes around a payload";
    mainProgram = "pg-wrap";
  };
}
