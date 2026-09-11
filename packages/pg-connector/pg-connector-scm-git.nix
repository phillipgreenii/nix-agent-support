{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-scm-git";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # ./pg-connector-pr-github.nix's cmd/pg-connector-pr-github build — one
  # go.mod, one gomod2nix.toml, N mkGoApp calls building N different
  # binaries out of it; this packet does not create a second Go module
  # (layout_convention_test.go).
  #
  # Filtered src (pg2-p5at3): `go list -deps`/`-test -deps` against
  # ./cmd/pg-connector-scm-git confirms its ENTIRE build+test dependency graph is
  # go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout's top-level package
  # (not its schemas/ or conformance/ subpackages — those are pulled in only by
  # pg-connector's own Tier-1 conformance suite and pkg/scriptout's own tests, not
  # by this binary), pkg/provider's root iface.go plus its own pkg/provider/scm
  # capability subpackage, and its own cmd/pg-connector-scm-git/ tree (main.go,
  # internal/**, testdata/**). None of the other 3 backends' cmd/pg-connector-*/
  # trees are reachable from here (verified: no cross-backend import, no
  # filesystem reference to a sibling backend's path) — scoping src to exactly
  # this set means editing pg-connector-issue-beads/-pr-github/-ci-github-actions'
  # own files no longer touches this derivation's content hash. Technique:
  # `lib.fileset.toSource`/`unions`/`difference`, the same fileset idiom
  # `phillipg-nix-repo-base`'s `mkGoApp` Pattern B and this repo's own
  # `packages/pg-router/default.nix` already use, adapted here for per-binary
  # isolation within one shared go.mod rather than a local module replace.
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
      ./pkg/provider/scm
      ./cmd/pg-connector-scm-git
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-scm-git" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and pg-connector-pr-github.nix's own
  # cmd/pg-connector*/main.go convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (unlike default.nix's cmd/pg-connector
  # build): this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # (actors.md's ACTOR-BACKEND).
  #
  # No wrapProgram for `git`: this backend execs `git` on PATH at runtime —
  # `git` is provisioned once, separately, wherever this workspace's
  # home-manager profile installs it, not per-consumer here, mirroring
  # pg-connector-pr-github.nix's own no-wrapProgram-for-gh rationale.

  meta = with lib; {
    description = "pg-connector's scm capability Tier-2 backend for local git — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
