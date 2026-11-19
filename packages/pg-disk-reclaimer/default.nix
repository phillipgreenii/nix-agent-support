{
  pkgs,
  bashBuilders,
}:
let
  pg-disk-reclaimer = pkgs.callPackage ./pg-disk-reclaimer {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pg-disk-reclaimer;
  inherit (pg-disk-reclaimer) packages tldr;
  checks = {
    # A Linux sandbox has a working ps (procps), so a sweep case that cannot run
    # there is a bug: PGDR_REQUIRE_SWEEP_TOOLS=1 makes it fail instead of skip
    # (tests/sweep-tools.bash, bead pg2-sbvin). The darwin sandbox denies ps, so
    # there the cases still skip and the `pg-disk-reclaimer-tmp-sweep-test` hook
    # (scripts/run-tmp-sweep-bats.sh) is the gate that really runs them.
    test-pg-disk-reclaimer =
      if pkgs.stdenv.hostPlatform.isLinux then
        pg-disk-reclaimer.check.overrideAttrs (_: {
          PGDR_REQUIRE_SWEEP_TOOLS = "1";
        })
      else
        pg-disk-reclaimer.check;

  };

  # Opt-in tier (PGDR_INTEGRATION=1): cases that need a real go or a real
  # OpenTelemetry collector. NOT a check: it is exposed separately so the flake
  # can publish it under `legacyPackages.<system>.integration-checks` (ADR 0024 in
  # nix-repo-base) and `nix flake check` never builds it. Build by name:
  # `nix build .#integration-checks.test-pg-disk-reclaimer-integration`. It is in no
  # hook and no run-unit-tests/pre-land gate (global rule V-4). The collector is
  # nixpkgs', which MAY differ from the one the observability stack pins; the
  # fixture config records both versions.
  integration-checks = {
    test-pg-disk-reclaimer-integration = pg-disk-reclaimer.check.overrideAttrs (old: {
      PGDR_INTEGRATION = "1";
      nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [
        pkgs.go
        pkgs.opentelemetry-collector-contrib
      ];
    });
  };
}
