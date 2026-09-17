{
  pkgs,
  lib,
  mkGoApp,
  makeWrapper,
  ccpool,
  bd,
  pg-pr,
  # jq is pkgs.jq (auto via callPackage) — a plain nixpkgs runtime dep, so unlike
  # `bd` it needs no explicit pass at the callPackage site in flake.nix.
  jq,
  # pg-connector resolves automatically via callPackage against
  # `final.pg-connector` (defined earlier in the same flake.nix overlay,
  # mirroring pg-desk's own local-replace comment) — no explicit pass needed
  # at the callPackage site, same as jq above.
  pg-connector,
  # pg-connector's own Tier-2 backend binaries (bead pg2-sh024's follow-up:
  # even with `pg-connector` itself on PATH, pg-connector in turn execs one
  # of these, by name, as a SECOND nested subprocess — ambient $PATH plugin
  # discovery per ADR 0062 — whenever a `command`-type source actually
  # queries it. That second-level exec inherits pg-router's PATH exactly as
  # `pg-connector` itself does, so every backend pg-connector can dispatch to
  # must also be on THIS wrapper's PATH. Each resolves automatically via
  # callPackage against the matching `final.pg-connector-*` overlay entry,
  # same as `pg-connector` above.
  pg-connector-pr-github,
  pg-connector-ci-github-actions,
  pg-connector-issue-beads,
  pg-connector-issue-jira,
  pg-connector-scm-git,
}:

mkGoApp {
  pname = "pg-router";

  # The module uses `replace ../ccpool` and `replace ../claude-transcript`, so the
  # build sandbox must contain ALL THREE package dirs at their relative positions.
  # ccpool itself replaces ../claude-transcript, so that dir must be present too
  # (it already is for pg-router's own use) for ccpool's replace to resolve.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      # ./docs holds the behavior docs (docs/behavior/), not build inputs. Exclude
      # them so a doc-only edit does not change the per-source digest and needlessly
      # rebump the pg-router version (repo CLAUDE.md "Versioning"; ADR 0025).
      (lib.fileset.difference ./. ./docs)
      ../ccpool
      ../claude-transcript
    ];
  };
  modRoot = "pg-router";

  # gomod2nix engine (ADR 0008, Case B): buildGoApplication symlinks the
  # first-party local-replace modules (../ccpool, ../claude-transcript) from
  # source — live, no vendorHash, no localReplaceModules overlay. The toml tracks
  # only third-party deps; ccpool/claude-transcript are intentionally absent from it.
  gomod2nixToml = ./gomod2nix.toml;

  # cmd/pg-router/main.go declares the version global in LOWERCASE
  # (`var version = "dev"`), but mkGoApp's default ldflag target is capital-V
  # `main.Version` (go-builders.nix, matching the fleet `var Version`
  # convention). Without this override `-X main.Version=` targets a symbol the
  # code never declares, the linker silently drops it, and pg-router --version
  # always reports the "dev" fallback. Guarded by the
  # `test-pg-router-version-stamped` flake check.
  versionPath = "main.version";

  nativeBuildInputs = [ makeWrapper ];

  # No subPackages is set above, so this derivation's gomod2nix checkPhase runs the full
  # `go test ./...`, including internal/config's git-common-dir tests (pg2-xl659), which build
  # a real throwaway repo + linked worktree via the `git` binary rather than skipping when it
  # is absent. Matches ccpool's own nativeCheckInputs = [ pkgs.git ] (packages/ccpool/default.nix),
  # which mirrors pg-pr's (packages/pg-pr/default.nix).
  nativeCheckInputs = [ pkgs.git ];

  # This list MUST carry the backing command of EVERY source and handler kind
  # pg-router can validate, because `Config.Load()`'s pre-runtime check 5
  # (`absentBackingCommands` in internal/config/config.go, required by
  # `INV-WORKFLOW-1` in docs/behavior/invariants.md) resolves each configured
  # participant's `BackingCommand()` and turns an ABSENT one into a pre-flight
  # FAILURE, not a runtime warning. A gap here MUST NOT be judged by running the
  # wrapper from a shell: `--prefix PATH` keeps the inherited PATH, so an interactive
  # session masks the gap and only a minimal-PATH context (launchd/service) fails.
  # Entries and what each satisfies:
  #   ccpool -> the `ccpool` handler role (`config.CCPoolCommand`)
  #   bd     -> pg-router's own first-class beads integration: the in-Go built-in
  #             default query set (`roles.BuiltinQuerySet` -> `query.BeadsReady`,
  #             `beads.Command`) plus reconcile/pgrouteracl/orchestrator's direct
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
  #   pg-connector -> the bare `pg-connector` binary that the
  #             pg-router-source-pg-connector adapter execs as a subprocess
  #             (ambient $PATH, no compile-time dependency — see that
  #             package's own default.nix) for the `command`-type sources
  #             backing worker/review/feedback/issue-beads-work. That
  #             adapter is itself spawned as a subprocess of pg-router and
  #             inherits pg-router's PATH, so pg-connector's bin dir must be
  #             on THIS wrapper's PATH for the nested exec to resolve (bead
  #             pg2-sh024: the live daemon's own process PATH, captured via
  #             `ps eww`, had ccpool/bd/pg-pr/jq but no pg-connector bin
  #             dir, so every producer tick for those sources failed with
  #             "exec: pg-connector: executable file not found in $PATH").
  #   pg-connector-pr-github/-ci-github-actions/-issue-beads/-issue-jira/
  #   -scm-git -> pg-connector's own Tier-2 backend binaries (ADR 0062),
  #             which `pg-connector` itself execs by name (again ambient
  #             $PATH plugin discovery, again inheriting THIS wrapper's
  #             PATH) once a query actually reaches it. Even with the
  #             pg2-sh024 fix above landed and applied, the live daemon's
  #             PATH still lacked every one of these — confirmed live via
  #             `env -i PATH=<daemon PATH> pg-connector issue list ...`
  #             reproducing `{"status":"degraded","reason":"scriptout:
  #             pg-connector-issue-beads: exec: \"pg-connector-issue-beads\":
  #             executable file not found in $PATH"}` — so EVERY producer
  #             tick still failed, including the GitHub-backed sources
  #             (pr-mine/pr-team/pr-sweep need pg-connector-pr-github), not
  #             just the bd-backed ones pg2-sh024 already fixed.
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
    wrapProgram $out/bin/pg-router --prefix PATH : ${
      lib.makeBinPath [
        ccpool
        bd
        pg-pr
        jq
        pg-connector
        pg-connector-pr-github
        pg-connector-ci-github-actions
        pg-connector-issue-beads
        pg-connector-issue-jira
        pg-connector-scm-git
      ]
    }
  '';

  meta = {
    description = "pg-router orchestrator (delegates claude+tmux to ccpool)";
    mainProgram = "pg-router";
  };
}
