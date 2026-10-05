{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-mail-osx-bridge";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of it
  # (layout_convention_test.go); this does not create a second Go module.
  #
  # Filtered src, mirroring pg-connector-calendar-osx-bridge.nix's own
  # per-binary isolation fileset (NOT the umbrella default.nix's "include
  # everything" convention): go.mod/go.sum/gomod2nix.toml, pkg/schema and
  # pkg/scriptout WHOLE (including pkg/scriptout/conformance, imported on
  # purpose by this binary's own
  # cmd/pg-connector-mail-osx-bridge/conformance_test.go), pkg/provider's
  # root iface.go, this binary's own pkg/provider/mail capability
  # subpackage, plus the pkg/provider/attention and pkg/provider/search
  # capability subpackages its own cmd/pg-connector-mail-osx-bridge/main.go
  # merges into one dispatch table, and its own
  # cmd/pg-connector-mail-osx-bridge/ tree (main.go, internal/**,
  # conformance_test.go). No cross-backend import exists, so editing any
  # sibling backend's own files does not touch this derivation's content
  # hash.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./pkg/schema
      ./pkg/scriptout
      ./pkg/provider/iface.go
      ./pkg/provider/mail
      ./pkg/provider/attention
      ./pkg/provider/search
      ./cmd/pg-connector-mail-osx-bridge
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-mail-osx-bridge" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall, no wrapProgram (matches
  # pg-connector-calendar-osx-bridge.nix and every other Tier-2 backend):
  # this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly. It talks only to
  # pg-osx-bridge-api's already-installed local Unix-domain socket (its
  # mail service) — no runtime dependency to wrap in.

  meta = with lib; {
    description = "pg-connector's mail capability Tier-2 backend, transporting over pg-osx-bridge-api's local Unix-domain socket (Mail.app-backed) — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
