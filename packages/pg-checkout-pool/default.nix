{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-checkout-pool";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single stdlib-only module
  # rooted at this package dir, no local `replace`, modRoot or parent-rooted
  # fileset (same shape as packages/pg-router-disk-watchdog).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-checkout-pool" ];

  meta = {
    description = "Bounded pool of reusable repository checkouts: per-slot lock, atomic lease records and slot state machine, with a status command";
    mainProgram = "pg-checkout-pool";
  };
}
