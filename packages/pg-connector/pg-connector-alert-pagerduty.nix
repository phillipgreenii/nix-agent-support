{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-alert-pagerduty";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix (one go.mod, one gomod2nix.toml, N
  # mkGoApp calls building N binaries out of it); bead pg2-t79bc does not
  # create a second Go module.
  #
  # Filtered src, mirroring pg-connector-alert-grafana.nix: go.mod/go.sum/
  # gomod2nix.toml, pkg/schema and pkg/scriptout WHOLE (including
  # pkg/scriptout/conformance, imported by this binary's own integration-tier
  # conformance test), pkg/provider's root iface.go (the AuthChecker type this
  # backend asserts), the pkg/provider/alert and pkg/provider/attention
  # capability subpackages its main.go merges into one dispatch table, and its
  # own cmd/pg-connector-alert-pagerduty/ tree. No cross-backend import exists,
  # so editing a sibling backend does not touch this derivation's hash.
  src = lib.fileset.toSource {
    root = ./.;
    # Generated from the binary's Go import graph by ./gen-filesets.sh (kept
    # current by the pg-connector-filesets pre-commit hook); never hand-list.
    fileset = lib.fileset.unions (
      [
        ./go.mod
        ./go.sum
        ./gomod2nix.toml
      ]
      ++ map (p: ./. + "/${p}") (import ./filesets/pg-connector-alert-pagerduty.nix)
    );
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-alert-pagerduty" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring every sibling backend's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall, no wrapProgram (matches every other
  # Tier-2 backend): this binary speaks only the scriptout wire protocol. The
  # one external program it may run is macOS `security` (an absolute-path
  # system tool, absent on other platforms, where the PAGERDUTY_API_TOKEN
  # fallback applies), so there is no nix runtime dependency to wrap in.

  meta = with lib; {
    description = "pg-connector's alert capability PagerDuty Tier-2 backend, reading firing incidents from the PagerDuty REST API v2 — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
