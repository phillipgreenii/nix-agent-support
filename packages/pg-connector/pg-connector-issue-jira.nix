{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-issue-jira";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # ./pg-connector-issue-beads.nix's sibling backend — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of it;
  # this packet does not create a second Go module (layout_convention_test.go).
  #
  # Filtered src, mirroring pg-connector-issue-beads.nix's own fileset scoping
  # (bead pg2-p5at3): this binary's entire build+test dependency graph is
  # go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout's top-level package
  # (not its schemas/ or conformance/ subpackages), pkg/provider's root
  # iface.go plus its own pkg/provider/issue, pkg/provider/search, AND
  # pkg/provider/attention capability subpackages (search added by bead
  # pg2-8hcnx; attention added by bead pg2-7wqkr: this binary now also wires
  # pkg/provider/attention.NewDispatchTable to answer the "list_attention"
  # op), and its own cmd/pg-connector-issue-jira/ tree (main.go, internal/**).
  # No cross-backend import exists, so editing
  # pg-connector-scm-git/-pr-github/-ci-github-actions/-issue-beads' own files
  # does not touch this derivation's content hash.
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
      ./pkg/provider/search
      ./pkg/provider/attention
      ./cmd/pg-connector-issue-jira
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-issue-jira" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (matches pg-connector-issue-beads.nix
  # and pg-connector-pr-github.nix): this binary speaks only the scriptout
  # wire protocol and has no independent CLI identity a human types directly.
  #
  # No wrapProgram for the generic Jira CLI this backend execs (pjira, from
  # phillipg-nix-repo-base): it is provisioned separately on PATH, matching
  # pg-connector-pr-github.nix's identical decision for `gh` and
  # pg-connector-issue-beads.nix's identical decision for `bd` — the binary is
  # installed once, wherever this workspace's own home-manager profile puts
  # it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's issue capability Tier-2 backend for a real external Jira integration — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
