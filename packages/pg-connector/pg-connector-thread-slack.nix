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
  # scoping (bead pg2-p5at3's precedent): this binary's entire
  # build+test dependency graph is go.mod/go.sum/gomod2nix.toml, pkg/schema,
  # pkg/scriptout's top-level package (not its schemas/ or conformance/
  # subpackages — mkGoApp runs no `go test` of its own, doCheck = false;
  # the whole-module `go test ./...` gate that DOES exercise this bead's own
  # conformance_test.go is checks.<system>.pg-connector-go-tests, which
  # reads the raw, unfiltered package dir, not this trimmed fileset),
  # pkg/provider's root iface.go (for pkg/provider.AuthChecker's type-check
  # in pkg/provider/thread/dispatch.go) plus its own pkg/provider/thread
  # capability subpackage, and its own cmd/pg-connector-thread-slack/ tree
  # (main.go, internal/**). No cross-backend import exists, so editing
  # pg-connector-scm-git/-pr-github/-ci-github-actions/-issue-beads/-issue-jira's
  # own files does not touch this derivation's content hash.
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
