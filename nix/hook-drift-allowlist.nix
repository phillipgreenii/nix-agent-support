# Allowlist for the hook drift guard (`phillipgreenii.pre-commit.driftGuard`,
# pg-hooks-drift-guard in phillipg-nix-repo-base; bead pg2-vagsu). Every entry is
# explicit: a path glob (matched against the repo-relative path; `*` also crosses
# `/`), the categories it allows, and the reason. The guard's patterns are never
# loosened. Keep the reasons free of tabs and newlines, and free of the words the
# guard itself detects, because this file is part of the scanned source tree.
#
# Categories: githooks-dir, githooks-ref, relative-hooks-path, config-link,
# removed-machinery.
[
  {
    path = ".gitignore";
    categories = [ "config-link" ];
    reason = "ADR 0016: the exact-line ignore rule for the old generated config, kept until every clone has dropped it; the comment above the rule explains why";
  }
  {
    path = "home/programs/agent-rules/pgii-agent-rules.md";
    categories = [ "config-link" ];
    reason = "current-truth agent rule text: it tells agents never to probe for hook presence by testing for the old config file, which is the legacy fallback probe";
  }
  {
    path = "docs/superpowers/plans/2026-08-25-drain-beads-context-diet.md";
    categories = [ "config-link" ];
    reason = "frozen point-in-time plan snapshot; it describes the old config link code as it was before the hook bundle";
  }
  {
    path = "docs/superpowers/specs/2026-08-25-drain-beads-context-diet-design.md";
    categories = [ "config-link" ];
    reason = "frozen point-in-time design snapshot; it describes the old gitignored config link as it was before the hook bundle";
  }
  {
    path = "packages/pg-router/internal/config/config_test.go";
    categories = [ "config-link" ];
    reason = "test comment citing the old config link as the precedent the read-through design deliberately differs from";
  }
  {
    path = "packages/wtnew/wtnew/tests/test-wtnew.bats";
    categories = [ "config-link" ];
    reason = "fixture: an old config link planted in the canonical clone proves wtnew writes nothing into the new worktree";
  }
]
