{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-alert-grafana";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix — one go.mod, one gomod2nix.toml,
  # N mkGoApp calls building N different binaries out of it
  # (layout_convention_test.go); this bead (pg2-rejc3) does not create a
  # second Go module.
  #
  # Filtered src, mirroring pg-connector-calendar-osx-bridge.nix's per-binary
  # isolation fileset (NOT the umbrella default.nix's "include everything"
  # convention): go.mod/go.sum/gomod2nix.toml, pkg/schema and pkg/scriptout
  # WHOLE (including pkg/scriptout/conformance, imported on purpose by this
  # binary's own cmd/pg-connector-alert-grafana/conformance_test.go), pkg/
  # provider's root iface.go (for pkg/provider.AuthChecker-shaped type checks
  # in the alert/attention dispatch.go files below), the pkg/provider/alert
  # and pkg/provider/attention capability subpackages its own main.go merges
  # into one dispatch table, and its own cmd/pg-connector-alert-grafana/ tree
  # (main.go, internal/**, tests). No cross-backend import exists, so editing
  # any sibling backend's files does not touch this derivation's content
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
      ./pkg/provider/alert
      ./pkg/provider/attention
      ./cmd/pg-connector-alert-grafana
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-alert-grafana" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall, no wrapProgram (matches every other
  # Tier-2 backend): this binary speaks only the scriptout wire protocol and
  # has no independent CLI identity a human types directly. It talks only to
  # the Grafana named by its per-backend config's base_url, over plain HTTP
  # with no credential — no runtime dependency to wrap in.

  meta = with lib; {
    description = "pg-connector's alert capability Grafana Tier-2 backend, reading a local Grafana's Alertmanager v2 alerts API — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
