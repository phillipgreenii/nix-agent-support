{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  pg-rescue,
  pg-rescue-push-branch,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-rescue-resolve-conflict";
  src = ./.;
  description = "Resolve one resolve-conflict item: claim it, rebase its PR branch in a worktree under pg-rescue, push through pg-rescue-push-branch, close it";
  # The push helper is pinned: it is the single audited push path and the shim
  # runs under launchd / pg-router with a thin PATH. Deliberately NOT pinned:
  # `bd` (ambient machine PATH, see ../default.nix) and `pg-rescue` (the
  # caller's own binary and its config, like sync-projects).
  runtimeDeps = [
    coreutils
    jq
    git
    pg-rescue-push-branch
  ];
  # The suite runs the REAL git, pg-rescue, pg-rescue-push-branch and bd.
  testDeps = [
    coreutils
    jq
    git
    beads
    pg-rescue
    pg-rescue-push-branch
  ];
  inherit testSupport;
}
