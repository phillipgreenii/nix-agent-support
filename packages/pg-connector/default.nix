{
  pkgs,
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector";

  # Filtered src (pg2-p5at3): this Tier-1 umbrella's OWN test suite is a
  # deliberate whole-module architecture-conformance check — layout_convention_test.go,
  # naming_convention_test.go, entity_store_test.go, backend_internal_sync_test.go,
  # chokepoint_test.go, dependency_direction_test.go, identifier_allowlist_test.go and
  # stack_readonly_test.go all `filepath.Abs("../..")`/walk the MODULE ROOT to verify
  # cross-backend layout/naming/isolation invariants over every cmd/pg-connector-*
  # tree and pkg/*, so (unlike the four Tier-2 backends below) this derivation's `src`
  # cannot be scoped down to only cmd/pg-connector's own dependency graph without
  # breaking those tests — they exist specifically to inspect the OTHER backends'
  # trees. `./cmd` and `./pkg` are therefore included whole (every backend and every
  # shared package), which is exactly what those tests need and nothing more: the only
  # things excluded are `./docs` (prose behavior docs, read by no test or build) and
  # the five `*.nix` derivation files themselves (irrelevant to `go build`/`go test`).
  # This still means an edit to ANY backend's Go source legitimately re-digests this
  # derivation — that is correct, not a bug: TestBackendLayoutConvention etc. must
  # re-verify the invariant over the new content. What this DOES fix (the bead's own
  # motivating example) is that a comment edit to a `.nix` file no longer touches any
  # of the 5 derivations' hashes at all, and each Tier-2 backend below is now isolated
  # from its siblings. Technique: `lib.fileset.toSource`/`unions`, the same fileset
  # idiom `phillipg-nix-repo-base`'s `mkGoApp` Pattern B and this repo's own
  # `packages/pr-pool/default.nix` (excluding `./docs`) already use — adapted here for
  # per-binary isolation within one shared go.mod rather than a local module replace.
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./cmd
      ./pkg
    ];
  };

  # gomod2nix engine (ADR 0008, Case A): no local replace, so third-party
  # deps are pinned in gomod2nix.toml and buildGoApplication builds from
  # src — no vendorHash FOD.
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring pg-pr/default.nix.
  versionPath = "main.Version";

  nativeBuildInputs = [ pkgs.help2man ];

  # Hard failures, no `|| true` (bead pg2-z28y9): the previous `|| true` on
  # each of these 4 generators swallowed any failure, so a cobra/help2man
  # regression could ship an empty or stale man page/completion with
  # nothing asserting non-empty output — and nothing in `nix flake check`
  # forced this package to build at all until the pg-connector-* checks
  # added alongside this fix started referencing it. Matches the
  # established convention in this repo's OTHER postInstall shell-completion
  # generation (packages/claude-extended-tool-approver/default.nix has no
  # `|| true`/`2>/dev/null` guard on its own completion generators) —
  # pg-pr/default.nix still carries the old guarded form and is out of this
  # bead's scope, but is the same latent defect.
  postInstall = ''
    # Generate man page
    mkdir -p $out/share/man/man1
    help2man --no-info --no-discard-stderr $out/bin/pg-connector \
      > $out/share/man/man1/pg-connector.1

    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/pg-connector completion bash > $out/share/bash-completion/completions/pg-connector
    $out/bin/pg-connector completion zsh > $out/share/zsh/site-functions/_pg-connector
    $out/bin/pg-connector completion fish > $out/share/fish/vendor_completions.d/pg-connector.fish
  '';

  meta = with lib; {
    description = "Unified pluggable connector umbrella CLI (pg-connector)";
    platforms = platforms.all;
  };
}
