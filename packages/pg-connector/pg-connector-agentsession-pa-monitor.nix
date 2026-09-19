{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-agentsession-pa-monitor";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of
  # it; this packet does not create a second Go module (layout_convention_test.go).
  #
  # Filtered src, mirroring pg-connector-issue-beads.nix's own fileset
  # pattern (the multi-capability precedent, pg2-7wqkr) rather than
  # pg-connector-thread-slack.nix's whole-pkg/scriptout variant: this
  # backend answers for THREE capabilities (agentsession, attention,
  # search), so its src fileset names all three of pkg/provider/agentsession,
  # pkg/provider/attention, and pkg/provider/search alongside go.mod/
  # go.sum/gomod2nix.toml, pkg/schema, pkg/scriptout's top-level package
  # (not its schemas/ or conformance/ subpackages -- those are pulled in
  # only by pg-connector's own Tier-1 conformance suite and pkg/scriptout's
  # own tests, not by this binary), pkg/provider's root iface.go, and its
  # own cmd/pg-connector-agentsession-pa-monitor/ tree (main.go,
  # internal/**). None of the other backends' cmd/pg-connector-*/ trees are
  # reachable from here (no cross-backend import, no filesystem reference
  # to a sibling backend's path) -- scoping src to exactly this set means
  # editing a sibling backend's own files no longer touches this
  # derivation's content hash.
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
      ./pkg/provider/agentsession
      ./pkg/provider/attention
      ./pkg/provider/search
      ./cmd/pg-connector-agentsession-pa-monitor
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-agentsession-pa-monitor" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (matches pg-connector-issue-beads.nix):
  # this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # (actors.md's ACTOR-BACKEND).
  #
  # No wrapProgram for `pa-monitor`: this backend execs `pa-monitor` on
  # PATH at runtime (cmd/pg-connector-agentsession-pa-monitor/internal/runner.go),
  # matching pg-connector-issue-beads.nix's identical decision for `bd` and
  # pg-connector-pr-github.nix's identical decision for `gh` -- `pa-monitor`
  # is provisioned once, separately, wherever this workspace's home-manager
  # profile installs it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector Tier-2 backend: agentsession capability, backed by pa-monitor";
    platforms = platforms.all;
  };
}
