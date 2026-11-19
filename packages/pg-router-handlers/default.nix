{
  lib,
  mkGoApp,
}:

let
  members = import ../pg-router/members.nix { inherit lib; };
in
mkGoApp {
  pname = "pg-router-ccpool-handler";

  # Pattern B (`phillipg-nix-repo-base` ADR 0008): these binaries are built from
  # the router module (packages/pg-router, one go.mod). The fileset is exactly the
  # handlers member's cmd directories, its internal/handlers subtree, its
  # conformance tests, the shared set and the module files (members.json lists
  # them; the Go boundary test reads the same file), so a router-engine edit does
  # not bump this package's `--version` and a handlers-only edit does not bump the
  # router's. The modules the router's go.mod replaces locally for the handlers
  # (`../ccpool` for the pool client and its sessionmeta test helper,
  # `../pg-claude-code-recorder`, `../pg-sqlite`, `../pg-failsig`) and
  # `../claude-transcript` (internal/usage's transcript reader and ccpoolRun's
  # needs_input detection) sit beside the module in one store copy.
  src = lib.fileset.toSource {
    root = members.packagesRoot;
    fileset = lib.fileset.unions [
      (members.memberFileset "handlers")
      ../ccpool
      ../pg-claude-code-recorder
      ../pg-sqlite
      ../pg-failsig
    ];
  };
  modRoot = "pg-router";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

  gomod2nixToml = ../pg-router/gomod2nix.toml;

  # Both cmd/ binaries declare `var version = "dev"` in LOWERCASE, but mkGoApp's
  # default ldflag target is capital-V `main.Version`; without this override the
  # linker silently drops the symbol and --version reports "dev". Same trap and
  # fix as packages/pg-router/default.nix; guarded by the
  # `test-pg-router-handler-ccpool-version-stamped` flake check.
  versionPath = "main.version";

  # Build exactly the two shipped binaries (pinning only one would silently drop
  # the other). The build runs no tests (mkGoApp defaults doCheck = false); the
  # `pg-router-ccpool-handler-go-tests` check is the test gate and supplies git
  # itself via testDeps. The testdataguard test fails if a package in either
  # binary's dependency closure embeds a testdata/ path.
  subPackages = [
    "cmd/pg-router-handler-ccpool"
    "cmd/pg-router-handler-command"
  ];

  meta = {
    description = "pg-router participant implementations (ccpool-backed and command-backed handlers), speaking INTF-HANDLER/INTF-SOURCE over the wire";
  };
}
