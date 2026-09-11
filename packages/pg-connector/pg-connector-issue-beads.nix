{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-issue-beads";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # ./pg-connector-pr-github.nix's sibling backend — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of
  # it; this packet does not create a second Go module (layout_convention_test.go).
  #
  # Filtered src (pg2-p5at3): `go list -deps`/`-test -deps` against
  # ./cmd/pg-connector-issue-beads confirms its ENTIRE build+test dependency graph
  # is go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout's top-level package
  # (not its schemas/ or conformance/ subpackages — those are pulled in only by
  # pg-connector's own Tier-1 conformance suite and pkg/scriptout's own tests, not
  # by this binary), pkg/provider's root iface.go plus its own pkg/provider/issue
  # AND pkg/provider/attention capability subpackages (attention added by bead
  # pg2-7wqkr: this binary now also wires pkg/provider/attention.NewDispatchTable
  # to answer the "list_attention" op), and its own cmd/pg-connector-issue-beads/
  # tree (main.go, internal/**). None of the other 3 backends' cmd/pg-connector-*/
  # trees are reachable from here (verified: no cross-backend import, no filesystem
  # reference to a sibling backend's path) — scoping src to exactly this set means
  # editing pg-connector-scm-git/-pr-github/-ci-github-actions' own files no longer
  # touches this derivation's content hash. Technique: `lib.fileset.toSource`/
  # `unions`/`difference`, the same fileset idiom `phillipg-nix-repo-base`'s
  # `mkGoApp` Pattern B and this repo's own `packages/pg-router/default.nix` already
  # use, adapted here for per-binary isolation within one shared go.mod rather
  # than a local module replace.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg/schema
      (lib.fileset.difference ./pkg/scriptout (
        lib.fileset.unions [
          ./pkg/scriptout/schemas
          ./pkg/scriptout/conformance
        ]
      ))
      ./pkg/provider/iface.go
      ./pkg/provider/issue
      ./pkg/provider/attention
      ./cmd/pg-connector-issue-beads
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-issue-beads" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and pg-connector-pr-github.nix's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (matches pg-connector-pr-github.nix):
  # this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # (actors.md's ACTOR-BACKEND).
  #
  # No wrapProgram for `bd`: this backend execs `bd` on PATH at runtime,
  # matching pg-connector-pr-github.nix's identical decision for `gh` — `bd`
  # is provisioned once, separately, wherever this workspace's home-manager
  # profile installs it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's issue capability Tier-2 backend for this workspace's own bd tracker — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
