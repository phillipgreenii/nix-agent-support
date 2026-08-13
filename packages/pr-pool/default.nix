{
  lib,
  mkGoApp,
  makeWrapper,
  ccpool,
  bd,
  pg-pr,
  # gh is pkgs.gh (auto via callPackage) — a plain nixpkgs runtime dep, so unlike
  # `bd` it needs no explicit pass at the callPackage site in flake.nix.
  gh,
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
  #   bd     -> the `beads-ready` / `beads-list` sources (`beads.Command`)
  #   pg-pr  -> `pg-pr config show` self_login resolution and `pg-pr pr list` (reconcile ACL)
  #   gh     -> the `github-issues` source (`query.ghCommand`)
  #
  # The `jira-issues` source's backing command (`query.jiraCommand` =
  # `pg-pr-issues-jira-zr`) MUST NOT be added here. It is a SEPARATE derivation,
  # defined only in `phillipg-nix-ziprecruiter` (`modules/pg-pr-zr/`), which is
  # DOWNSTREAM of this flake — naming it here would invert the dependency direction,
  # and it is NOT provided by the `pg-pr` package above (that store path's `bin/`
  # holds only `pg-pr`). A deployment that enables a jira-issues source MUST supply
  # that command itself, from the downstream flake that defines it. Check 5 MUST NOT
  # be weakened, special-cased, or exempted to accommodate the gap.
  postInstall = ''
    wrapProgram $out/bin/pr-pool --prefix PATH : ${
      lib.makeBinPath [
        ccpool
        bd
        pg-pr
        gh
      ]
    }
  '';

  meta = {
    description = "PR-pool orchestrator (delegates claude+tmux to ccpool)";
    mainProgram = "pr-pool";
  };
}
