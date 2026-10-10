{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-calendar-task-focus";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix (one go.mod, one gomod2nix.toml, N
  # mkGoApp calls building N binaries out of it; layout_convention_test.go).
  #
  # Filtered src, mirroring pg-connector-calendar-osx-bridge.nix's per-binary
  # isolation fileset: go.mod/go.sum/gomod2nix.toml, pkg/schema, pkg/eventlog
  # (the shared writer behind this backend's own event log) and pkg/scriptout
  # WHOLE (including pkg/scriptout/conformance, imported on purpose by this
  # binary's own cmd/pg-connector-calendar-task-focus/conformance_test.go), the
  # root pkg/provider/iface.go (for the provider.AuthChecker-shaped type checks
  # in the dispatch.go files below), the calendar and attention capability
  # subpackages its own main.go merges into one dispatch table, and its own
  # cmd/pg-connector-calendar-task-focus/ tree. No cross-backend import exists,
  # so editing a sibling's files does not touch this derivation's content hash.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg/schema
      ./pkg/eventlog
      ./pkg/scriptout
      ./pkg/provider/iface.go
      ./pkg/provider/calendar
      ./pkg/provider/attention
      ./cmd/pg-connector-calendar-task-focus
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-calendar-task-focus" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall, no wrapProgram (matches every other
  # Tier-2 backend): this binary speaks only the scriptout wire protocol and has
  # no independent CLI identity a human types directly. It talks only to the
  # pg-task-focus daemon's loopback HTTP API, so there is no runtime dependency
  # to wrap in.

  meta = with lib; {
    description = "pg-connector's calendar and attention Tier-2 backend for the pg-task-focus daemon, reading its loopback HTTP API — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
