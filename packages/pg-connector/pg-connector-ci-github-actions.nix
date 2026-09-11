{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-ci-github-actions";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build —
  # one go.mod, one gomod2nix.toml, N mkGoApp calls building N different
  # binaries out of it (this is the ci capability's Tier-2 GitHub Actions
  # backend, sibling to pg-connector-pr-github.nix); this packet does not
  # create a second Go module (layout_convention_test.go).
  #
  # Filtered src (pg2-p5at3): `go list -deps`/`-test -deps` against
  # ./cmd/pg-connector-ci-github-actions confirms its ENTIRE build+test
  # dependency graph is go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout's
  # top-level package (not its schemas/ or conformance/ subpackages — those are
  # pulled in only by pg-connector's own Tier-1 conformance suite and
  # pkg/scriptout's own tests, not by this binary), pkg/provider's root iface.go
  # plus its own pkg/provider/ci capability subpackage, and its own
  # cmd/pg-connector-ci-github-actions/ tree (main.go, internal/** — including
  # internal/gitenv, internal/github and their _test.go files). None of the
  # other 3 backends' cmd/pg-connector-*/ trees are reachable from here (verified:
  # no cross-backend import, no filesystem reference to a sibling backend's path
  # — the sha256-pinned drift guards comparing this backend's gitenv.go/
  # github/*.go against pg-connector-pr-github's copies live in cmd/pg-connector's
  # OWN test suite, not here) — scoping src to exactly this set means editing
  # pg-connector-scm-git/-issue-beads/-pr-github's own files no longer touches
  # this derivation's content hash. Technique: `lib.fileset.toSource`/`unions`/
  # `difference`, the same fileset idiom `phillipg-nix-repo-base`'s `mkGoApp`
  # Pattern B and this repo's own `packages/pg-router/default.nix` already use,
  # adapted here for per-binary isolation within one shared go.mod rather than a
  # local module replace.
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
      ./pkg/provider/ci
      ./cmd/pg-connector-ci-github-actions
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-ci-github-actions" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring pg-connector-pr-github.nix's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (unlike default.nix's cmd/pg-connector
  # build): this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # (actors.md's ACTOR-BACKEND; freedom boundary, part 4).
  #
  # No wrapProgram for `gh`: this backend execs it on PATH at runtime,
  # carried over unchanged from the ported GitHub Actions client —
  # provisioned once, separately, wherever this workspace's home-manager
  # profile installs it, matching pg-connector-pr-github.nix's own choice
  # not to wrap `gh`. This backend does NOT exec `pg-connector`
  # itself: its own PRResolver (resolver.go) resolves a PR id directly
  # against GitHub over the same `gh` gateway, never by shelling back into
  # the Tier-1 umbrella that dispatches it (INV-COMP-1; bug fix).

  meta = with lib; {
    description = "pg-connector's ci capability Tier-2 backend for GitHub Actions — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
