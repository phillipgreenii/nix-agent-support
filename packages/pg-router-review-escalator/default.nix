{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-review-escalator";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single stdlib-only module
  # rooted at this package dir. It execs `pg-connector` (the `pr review submit`
  # verb, and the `issue ...` verbs for the tracker) and a configured notify
  # command as subprocesses, so it has no compile-time dependency on another
  # package: no local `replace`, modRoot or parent-rooted fileset (same shape as
  # packages/pg-router-probe and packages/pg-router-disk-watchdog).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-router-review-escalator" ];

  meta = {
    description = "Escalation path for unremovable pending reviews: wraps pg-connector pr review submit and, on blocked_human_pending, raises one deduplicated human bead per PR plus a push notification (bead pg2-kftf9.15)";
    mainProgram = "pg-router-review-escalator";
  };
}
