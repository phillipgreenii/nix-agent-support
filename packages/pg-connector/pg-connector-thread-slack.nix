{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-thread-slack";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of it;
  # this bead does not create a second Go module (layout_convention_test.go).
  #
  # Filtered src, mirroring pg-connector-issue-jira.nix's own fileset
  # scoping (bead pg2-p5at3's precedent) — with ONE deliberate divergence
  # from every sibling backend: mkGoApp does NOT skip `go test` (doCheck is
  # left at buildGoApplication's real default, which runs goCheckHook
  # scoped to `subPackages`; see default.nix's own pg2-p5at3 comment for
  # where this was first learned the hard way). Every sibling backend's
  # `cmd/pg-connector-<name>/` test files happen not to import
  # pkg/scriptout/{schemas,conformance}, so trimming those two subpackages
  # out of `src` is invisible to them. This binary's own
  # cmd/pg-connector-thread-slack/conformance_test.go is the first to
  # import pkg/scriptout/conformance on purpose (its own doc comment:
  # deliberately not build-tag-gated, so it runs under plain `go test`),
  # and conformance.go itself imports pkg/scriptout/schemas — so both stay
  # IN `src` here, unlike every sibling's difference-based exclusion.
  # go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout WHOLE (not
  # trimmed), pkg/provider's root iface.go (for pkg/provider.AuthChecker's
  # type-check in pkg/provider/thread/dispatch.go) plus its own
  # pkg/provider/thread capability subpackage, and its own
  # cmd/pg-connector-thread-slack/ tree (main.go, internal/**,
  # conformance_test.go). No cross-backend import exists, so editing
  # pg-connector-scm-git/-pr-github/-ci-github-actions/-issue-beads/-issue-jira's
  # own files does not touch this derivation's content hash.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg/schema
      ./pkg/scriptout
      ./pkg/provider/iface.go
      ./pkg/provider/thread
      ./cmd/pg-connector-thread-slack
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-thread-slack" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (matches pg-connector-issue-jira.nix
  # and pg-connector-issue-beads.nix): this binary speaks only the
  # scriptout wire protocol and has no independent CLI identity a human
  # types directly.
  #
  # No wrapProgram for `claude`: it is provisioned separately on this
  # machine's own PATH (already configured, including its Slack MCP
  # server — this bead's own Objective/Files sections), matching
  # pg-connector-issue-jira.nix's identical decision for `pjira` and
  # pg-connector-pr-github.nix's identical decision for `gh`. The binary is
  # installed once, wherever this workspace's own home-manager profile puts
  # it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's thread capability Tier-2 backend, transporting over claude -p to the machine's already-configured Slack MCP — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
