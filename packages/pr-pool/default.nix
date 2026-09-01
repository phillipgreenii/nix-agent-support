{
  lib,
  mkGoApp,
  makeWrapper,
  ccpool,
  bd,
  pg-pr,
  # jq is pkgs.jq (auto via callPackage) — a plain nixpkgs runtime dep, so unlike
  # `bd` it needs no explicit pass at the callPackage site in flake.nix.
  jq,
}:

mkGoApp {
  pname = "pr-pool";

  # The module uses `replace ../ccpool` and `replace ../claude-transcript`, so the
  # build sandbox must contain ALL THREE package dirs at their relative positions.
  # ccpool itself replaces ../claude-transcript, so that dir must be present too
  # (it already is for pr-pool's own use) for ccpool's replace to resolve.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      # ./docs holds the behavior docs (docs/behavior/), not build inputs. Exclude
      # them so a doc-only edit does not change the per-source digest and needlessly
      # rebump the pr-pool version (repo CLAUDE.md "Versioning"; ADR 0025).
      (lib.fileset.difference ./. ./docs)
      ../ccpool
      ../claude-transcript
    ];
  };
  modRoot = "pr-pool";

  # gomod2nix engine (ADR 0008, Case B): buildGoApplication symlinks the
  # first-party local-replace modules (../ccpool, ../claude-transcript) from
  # source — live, no vendorHash, no localReplaceModules overlay. The toml tracks
  # only third-party deps; ccpool/claude-transcript are intentionally absent from it.
  gomod2nixToml = ./gomod2nix.toml;

  # cmd/pr-pool/main.go declares the version global in LOWERCASE
  # (`var version = "dev"`), but mkGoApp's default ldflag target is capital-V
  # `main.Version` (go-builders.nix, matching the fleet `var Version`
  # convention). Without this override `-X main.Version=` targets a symbol the
  # code never declares, the linker silently drops it, and pr-pool --version
  # always reports the "dev" fallback. Guarded by the
  # `test-pr-pool-version-stamped` flake check.
  versionPath = "main.version";

  nativeBuildInputs = [ makeWrapper ];

  # This list MUST carry the backing command of EVERY source and handler kind
  # pr-pool can validate, because `Config.Load()`'s pre-runtime check 5
  # (`absentBackingCommands` in internal/config/config.go, required by
  # `INV-WORKFLOW-1` in docs/behavior/invariants.md) resolves each configured
  # participant's `BackingCommand()` and turns an ABSENT one into a pre-flight
  # FAILURE, not a runtime warning. A gap here MUST NOT be judged by running the
  # wrapper from a shell: `--prefix PATH` keeps the inherited PATH, so an interactive
  # session masks the gap and only a minimal-PATH context (launchd/service) fails.
  # Entries and what each satisfies:
  #   ccpool -> the `ccpool` handler role (`config.CCPoolCommand`)
  #   bd     -> pr-pool's own first-class beads integration: the in-Go built-in
  #             default query set (`roles.BuiltinQuerySet` -> `query.BeadsReady`,
  #             `beads.Command`) plus reconcile/prpoolacl/orchestrator's direct
  #             `bd` calls. NOT a typed query surface any more (pg2-n75tk removed
  #             `beads-ready`/`beads-list` from the TOML query factory).
  #   pg-pr  -> `pg-pr config show` self_login resolution and `pg-pr pr list` (reconcile ACL)
  #   jq     -> generic JSON-shaping glue for `command`-type sources, e.g. the
  #             `sh -c 'bd ready ... | jq ...'` pipelines `config --print-defaults`
  #             emits as the built-in defaults' `command` equivalent
  #             (`internal/config/example.go`'s `beadsReadyCommand`). jq carries no
  #             tool-specific semantics of its own (unlike `gh` or a Jira CLI), so
  #             bundling it does not reintroduce "Core knows how another tool is
  #             configured" — it is exactly as generic as `sh`, which every
  #             `command`-type pipeline already assumes.
  #
  # `gh` was removed here (pg2-n75tk): it existed solely to back the typed
  # `github-issues` source, which is gone — the boundary principle (Core must
  # not know how another tool is configured) now applies to it exactly as it
  # already did to `jira-issues`. A deployment that wants a `command` source
  # invoking `gh`, a Jira CLI, or anything else MUST supply that command
  # itself, from its own wrapper/PATH — naming any such tool here would put
  # tool-specific knowledge back into this upstream flake. Check 5 MUST NOT be
  # weakened, special-cased, or exempted to accommodate a gap.
  postInstall = ''
    wrapProgram $out/bin/pr-pool --prefix PATH : ${
      lib.makeBinPath [
        ccpool
        bd
        pg-pr
        jq
      ]
    }
  '';

  meta = {
    description = "PR-pool orchestrator (delegates claude+tmux to ccpool)";
    mainProgram = "pr-pool";
  };
}
