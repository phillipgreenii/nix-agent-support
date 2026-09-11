{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-pr-github";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build —
  # one go.mod, one gomod2nix.toml, two mkGoApp calls building two different
  # binaries out of it; this packet does not create a second Go module
  # (layout_convention_test.go).
  #
  # Filtered src (pg2-p5at3): `go list -deps`/`-test -deps` against
  # ./cmd/pg-connector-pr-github confirms its ENTIRE build+test dependency graph
  # is go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout's top-level package
  # (not its schemas/ or conformance/ subpackages — those are pulled in only by
  # pg-connector's own Tier-1 conformance suite and pkg/scriptout's own tests, not
  # by this binary), pkg/provider's root iface.go plus its own pkg/provider/pr,
  # pkg/provider/search, AND pkg/provider/attention capability subpackages
  # (search added by bead pg2-8hcnx; attention added by bead pg2-7wqkr: this
  # binary now also wires pkg/provider/attention.NewDispatchTable to answer the
  # "list_attention" op, porting the mine-vs-team NeedsAttention CONCEPT from
  # packages/pg-pr/internal/snapshot/attention.go), and its own
  # cmd/pg-connector-pr-github/ tree (main.go, internal/** — including
  # internal/api, internal/gitenv, internal/vcs, internal/github and their
  # testdata). None of the other 3 backends' cmd/pg-connector-*/ trees are
  # reachable from here (verified: no cross-backend import, no filesystem
  # reference to a sibling backend's path — the sha256-pinned drift guards
  # comparing this backend's gitenv.go/github/*.go against
  # pg-connector-ci-github-actions' copies live in cmd/pg-connector's OWN test
  # suite, not here) — scoping src to exactly this set means editing
  # pg-connector-scm-git/-issue-beads/-ci-github-actions' own files no longer
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
      ./pkg/provider/pr
      ./pkg/provider/search
      ./pkg/provider/attention
      ./cmd/pg-connector-pr-github
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-pr-github" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's own cmd/pg-connector build.
  versionPath = "main.Version";

  # No help2man/completions postInstall (unlike default.nix's cmd/pg-connector
  # build): this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # (actors.md's ACTOR-BACKEND; freedom boundary, part 4).
  #
  # No wrapProgram for `gh`: this backend execs `gh` on PATH at runtime,
  # carried over unchanged from pg-pr's own github provider, which likewise
  # ships with no gh wrapping in packages/pg-pr/default.nix — `gh` is
  # provisioned once, separately, wherever this workspace's home-manager
  # profile installs it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's pr capability Tier-2 backend for GitHub — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
