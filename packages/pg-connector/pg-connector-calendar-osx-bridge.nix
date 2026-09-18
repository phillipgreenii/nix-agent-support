{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-calendar-osx-bridge";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of it
  # (layout_convention_test.go); this bead does not create a second Go
  # module.
  #
  # Filtered src, mirroring pg-connector-thread-slack.nix's own per-binary
  # isolation fileset (NOT the umbrella default.nix's "include everything"
  # convention): go.mod/go.sum/gomod2nix.toml, pkg/schema and pkg/scriptout
  # WHOLE (including pkg/scriptout/conformance, imported on purpose by this
  # binary's own cmd/pg-connector-calendar-osx-bridge/conformance_test.go,
  # mirroring cmd/pg-connector-thread-slack's identical choice), pkg/
  # provider's root iface.go (for pkg/provider.AuthChecker-shaped type
  # checks in the calendar/attention/search dispatch.go files below), this
  # binary's own new pkg/provider/calendar capability subpackage, plus the
  # pkg/provider/attention and pkg/provider/search capability subpackages
  # its own cmd/pg-connector-calendar-osx-bridge/main.go merges into one
  # dispatch table (mirrors cmd/pg-connector-issue-jira/main.go's identical
  # newDispatchTable/capabilitiesBase merge pattern, bead pg2-fh2vh), and
  # its own cmd/pg-connector-calendar-osx-bridge/ tree (main.go, internal/**,
  # conformance_test.go). No cross-backend import exists, so editing
  # pg-connector-scm-git/-pr-github/-ci-github-actions/-issue-beads/
  # -issue-jira/-thread-slack's own files does not touch this derivation's
  # content hash.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg/schema
      ./pkg/scriptout
      ./pkg/provider/iface.go
      ./pkg/provider/calendar
      ./pkg/provider/attention
      ./pkg/provider/search
      ./cmd/pg-connector-calendar-osx-bridge
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-calendar-osx-bridge" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall, no wrapProgram (matches
  # pg-connector-thread-slack.nix and every other Tier-2 backend): this
  # binary speaks only the scriptout wire protocol and has no independent
  # CLI identity a human types directly. It talks only to osx-bridge-api's
  # already-installed local Unix-domain socket [landed: pg2-p9ap3,
  # pg2-tk57n] — no runtime dependency to wrap in.

  meta = with lib; {
    description = "pg-connector's calendar capability Tier-2 backend, transporting over osx-bridge-api's local Unix-domain socket (EventKit-backed) — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
