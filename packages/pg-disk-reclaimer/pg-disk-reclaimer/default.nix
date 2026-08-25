{
  mkBashScript,
  pkgs,
}:
mkBashScript {
  name = "pg-disk-reclaimer";
  src = ./.;
  description = "Data-driven disk-space-reclaim CLI";
  # jq reads/validates the registry JSON file. pg2-txxyj.2 lands the
  # registry loading + validation engine (pgdr_read_registry /
  # pgdr_validate_registry in pg-disk-reclaimer.bash), which shells out to
  # jq directly.
  runtimeDeps = [
    pkgs.jq
  ];

  # The lib bats suite sources pg-disk-reclaimer.bash directly (raw source,
  # not the assembled+wrapped script), so it needs jq on PATH independently
  # of runtimeDeps' `--suffix` wrapper (which only applies to the shipped
  # binary). Without this, `check-pg-disk-reclaimer`'s sandboxed bats run
  # has no jq and every pgdr_* test that shells out to it fails.
  testDeps = [
    pkgs.jq
  ];
}
