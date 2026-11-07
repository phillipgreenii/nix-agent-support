{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-checkout-pool";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module (golang.org/x/sys only)
  # rooted at this package dir, no local `replace`, modRoot or parent-rooted
  # fileset (same shape as packages/pg-router-disk-watchdog).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-checkout-pool" ];

  # The tldr page, flat under share/tldr/pages.common (the layout pg-probe and
  # pg-keep-awake ship; a home module registers it through
  # programs.tldr.customPages).
  postInstall = ''
    mkdir -p $out/share/tldr/pages.common
    cp ${./share/tldr/pages.common/pg-checkout-pool.md} $out/share/tldr/pages.common/pg-checkout-pool.md
  '';

  meta = {
    description = "Bounded pool of reusable repository checkouts: per-slot lock, atomic lease records and slot state machine, with lease, renew, release, reap and status commands";
    mainProgram = "pg-checkout-pool";
  };
}
