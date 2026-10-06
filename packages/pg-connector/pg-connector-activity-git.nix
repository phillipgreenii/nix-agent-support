{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-activity-git";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # every sibling backend's own <name>.nix — one go.mod, one
  # gomod2nix.toml, N mkGoApp calls building N different binaries out of
  # it (layout_convention_test.go).
  #
  # Filtered src, mirroring pg-connector-scm-git.nix: go.mod/go.sum/
  # gomod2nix.toml, pkg/schema, pkg/scriptout's top-level package (not its
  # schemas/ or conformance/ subpackages), pkg/provider's root iface.go plus
  # pkg/provider/activity (the capability this backend implements; omitting
  # it breaks the build, the same bug fixed for other backends in commit
  # fc23211e), and its own cmd/pg-connector-activity-git/ tree. Every later
  # change that adds an import to this backend MUST extend this allowlist in
  # the same change (fileset_coverage_test.go enforces it).
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
      ./pkg/provider/activity
      ./cmd/pg-connector-activity-git
    ];
  };
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-activity-git" ];

  # Exported as `main.Version` (capitalised), mirroring every sibling
  # cmd/pg-connector*/main.go convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall and no wrapProgram: this binary
  # speaks only the scriptout wire protocol and has no independent CLI
  # identity; it execs `git` on PATH at runtime, provisioned once by the
  # home-manager profile, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's git commit activity capability-only Tier-2 backend — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
