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
  # Filtered src, mirroring pg-connector-github.nix: go.mod/go.sum/gomod2nix.toml,
  # the whole ./pkg tree and this binary's own cmd/pg-connector-issue-jira/ tree
  # (main.go, internal/**). The daemon imports the shared connector-daemon
  # library (pkg/connectord) and its helpers, so ./pkg is included whole and a
  # later change that adds a shared package under ./pkg needs no edit here;
  # cmd/pg-connector/fileset_coverage_test.go checks this allowlist covers the
  # binary's Go import closure. No cross-backend cmd/ import exists, so editing
  # another backend's cmd/ tree does not touch this derivation's content hash.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg
      ./cmd/pg-connector-issue-jira
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-issue-jira" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (matches pg-connector-issue-beads.nix
  # and pg-connector-github.nix): this binary speaks only the scriptout
  # wire protocol and has no independent CLI identity a human types directly.
  #
  # No wrapProgram for the generic Jira CLI this backend execs (pjira, from
  # phillipg-nix-repo-base): it is provisioned separately on PATH, matching
  # pg-connector-github.nix's identical decision for `gh` and
  # pg-connector-issue-beads.nix's identical decision for `bd` — the binary is
  # installed once, wherever this workspace's own home-manager profile puts
  # it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's issue capability Tier-2 backend for a real external Jira integration — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
