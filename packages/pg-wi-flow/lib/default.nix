{
  mkBashLibrary,
  pkgs,
}:
let
  # test-support: the shared hermetic-by-construction bats git-fixture
  # harness (vendored copy, see test-support/git-fixture-harness.bash's own
  # header), needed by test-config.bats's two git-toplevel-resolution
  # cases. Wired onto BOTH config and tracker below -- tracker's own check
  # ALSO runs test-config.bats in full (its composed lib includes
  # config.bash), not just config's own check.
  testSupport = ./test-support;

  # actor.bash: PG_WI_FLOW_IDENT + stage composition, or an explicit
  # --actor override (bead tc-q25wo item 2).
  actor = mkBashLibrary {
    name = "actor";
    src = ./.;
    description = "pg-wi-flow CLI actor composition: PG_WI_FLOW_IDENT + stage, or an explicit --actor override (bead tc-q25wo item 2)";
  };

  # config.bash: the two-layer JSON config loader (machine layer deep-merged
  # under a repo layer, repo wins) plus workflow/stage lookup helpers,
  # falling back to the built-in null workflow when none is configured
  # (tc-9ddu3.1.1).
  config = mkBashLibrary {
    name = "config";
    src = ./.;
    description = "pg-wi-flow two-layer JSON config loader ($XDG_CONFIG_HOME/pg-wi-flow/config.json deep-merged under <repo>/.claude/wi-flow/config.json, repo wins) and workflow/stage lookup helpers, with a built-in null-workflow fallback (tc-9ddu3.1.1)";
    # jq: every function here builds/reads JSON. git: pgwf_config_repo_path
    # resolves the repo layer via `git rev-parse --show-toplevel`; its bats
    # coverage exercises that against a throwaway, hermetically-fixtured
    # git repo (git-fixture-harness.bash). bash: unlike mkBashScript's own
    # check, mkBashLibrary's check does NOT put `bash` on PATH by default
    # -- needed here for `#!/usr/bin/env bash` mocks the tests spawn.
    testDeps = [
      pkgs.jq
      pkgs.git
      pkgs.bash
    ];
    inherit testSupport;
  };

  # tracker.bash: the bd Adapter -- the ONLY file in this package that
  # invokes `bd` (query building, item fetch, claim/release, the internal
  # stage-advance primitive `next`'s container descent and the later
  # `advance` verb share) (tc-9ddu3.1.1).
  tracker = mkBashLibrary {
    name = "tracker";
    src = ./.;
    description = "pg-wi-flow's bd Adapter -- the only file in this package that invokes \`bd\` (config-aware query builder, item fetch/update, claim/release, the internal stage-advance primitive) (tc-9ddu3.1.1)";
    libraries = [ config ];
    # jq + git + bash: config.bash's tests (see above) also run under
    # tracker's own check (its composed lib includes config.bash's
    # content); tracker's own tests spawn `#!/usr/bin/env bash` mocks too.
    testDeps = [
      pkgs.jq
      pkgs.git
      pkgs.bash
    ];
    inherit testSupport;
  };
  # context.bash: the render engine -- classification, WI_* line dump, and
  # the fully assembled prompt for context/explain/history/duplicates/docs
  # (tc-9ddu3.1.2). Depends on tracker (which already composes config
  # ahead of itself), so sourcing context's composed lib alone brings in
  # all three.
  context = mkBashLibrary {
    name = "context";
    src = ./.;
    description = "pg-wi-flow's render engine: classification, kind/topic/instructions/checklist/concerns/duplicates/docs/premise/siblings resolution, the WI_* line dump, and the fully assembled prompt (tc-9ddu3.1.2)";
    libraries = [ tracker ];
    # jq: every function here builds/reads JSON. git: pgwf_context_repo_root
    # resolves the data-overlay root the same way config.bash's
    # pgwf_config_repo_path does. bash: spawned #!/usr/bin/env bash mocks
    # in this file's own bats coverage.
    testDeps = [
      pkgs.jq
      pkgs.git
      pkgs.bash
    ];
    inherit testSupport;
  };
in
{
  inherit
    actor
    config
    tracker
    context
    ;
  checks = {
    test-pg-wi-flow-actor = actor.check;
    test-pg-wi-flow-config = config.check;
    test-pg-wi-flow-tracker = tracker.check;
    test-pg-wi-flow-context = context.check;
  };
}
