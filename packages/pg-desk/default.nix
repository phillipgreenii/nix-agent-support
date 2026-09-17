{
  lib,
  mkGoApp,
  makeWrapper,
  pg-connector,
}:

mkGoApp {
  pname = "pg-desk";

  # go.mod locally replaces ../pg-connector (internal/gather's own test
  # suite reuses pkg/scriptout/conformance's real wire-envelope schema
  # checker rather than hand-rolling a parallel one — docket pg2-2j5ac.32,
  # packet 4's own Files instruction), so the build sandbox must contain
  # both package dirs at their relative positions (Pattern B,
  # `phillipg-nix-repo-base` ADR 0008; mirrors packages/pg-router's own
  # local replace of `../ccpool`/`../claude-transcript` in
  # packages/pg-router/default.nix).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-connector
    ];
  };
  modRoot = "pg-desk";

  # gomod2nix engine (ADR 0008, Case B): buildGoApplication symlinks the
  # local-replace module (../pg-connector) from source — live, no
  # vendorHash, no localReplaceModules overlay. The toml tracks only
  # third-party deps; pg-connector is intentionally absent from it.
  gomod2nixToml = ./gomod2nix.toml;

  # No `subPackages` set (mirrors packages/pg-router/default.nix): the
  # gomod2nix checkPhase therefore runs the FULL `go test ./...` across
  # every internal/* package this docket's earlier packets (2-9) already
  # landed, not just cmd/pg-desk. This packet's own "nix build .#pg-desk"
  # validation command is thereby the whole-module Go test gate (repo
  # CLAUDE.md's package-versioning path-rule "Go test gate"; bead
  # pg2-3nb2t) — no separate checks.<system>.pg-desk-go-tests attribute is
  # needed for that.

  # This package exports its version as `main.Version` (capitalised) —
  # cmd/pg-desk/main.go's `var Version = "dev"` — matching mkGoApp's
  # default ldflag target, so no versionPath override is needed (unlike
  # packages/pg-router/default.nix, whose cmd declares lowercase
  # `main.version`).

  nativeBuildInputs = [ makeWrapper ];

  # D10 composition rule (docs/behavior/pg-desk/README.md "Composition
  # rule"): pg-desk execs no binary other than pg-connector and the
  # operator-configured browser opener, directly or transitively.
  # cmd/pg-desk/composition_test.go's chokepoint test enforces this at the
  # SOURCE level (pre-wrap, scanning for any OTHER literal exec.Command
  # binary name). This wrapProgram call is the other half — it is what
  # actually SUPPLIES pg-connector at runtime, resolved by its bare $PATH
  # name (internal/gather's own `pgConnectorBinary = "pg-connector"` /
  # cmd/pg-desk/doctor.go's `doctorLookPath("pg-connector")`), never a
  # compile-time import — mirroring packages/pg-router/default.nix's own
  # wrapProgram call for ccpool/bd/pg-pr/jq. Proven post-wrap by this
  # packet's own bats smoke test (packages/pg-desk/tests/composition-rule.bats),
  # which runs the nix-wrapped binary (this wrapper, pointed at a stub
  # pg-connector) against the unwrapped binary under `env -i` as a negative
  # control.
  postInstall = ''
    wrapProgram $out/bin/pg-desk --prefix PATH : ${lib.makeBinPath [ pg-connector ]}
  '';

  meta = {
    description = "pg-desk operator triage desk for PR/issue work surfaced through pg-connector";
    mainProgram = "pg-desk";
  };
}
