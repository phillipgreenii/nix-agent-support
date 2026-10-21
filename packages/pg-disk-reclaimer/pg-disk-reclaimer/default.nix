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
  # jq directly. coreutils' `timeout` bounds each item's displayCommand in
  # cmd_list (pgdr_display_output) so one slow/hung command can't dominate
  # the whole listing -- must be a declared dep rather than relying on
  # whatever happens to be on the invoking user's PATH, per this repo's
  # standalone/self-contained principle.
  runtimeDeps = [
    pkgs.jq
    pkgs.coreutils
  ];

  # The lib bats suite sources pg-disk-reclaimer.bash directly (raw source,
  # not the assembled+wrapped script), so it needs jq/timeout on PATH
  # independently of runtimeDeps' `--suffix` wrapper (which only applies to
  # the shipped binary). Without this, `check-pg-disk-reclaimer`'s sandboxed
  # bats run has no jq/timeout and every pgdr_* test that shells out to them
  # fails.
  #
  # tests/test-pg-disk-reclaimer-tmp-sweep.bats (bead pg2-rym7f) exercises the
  # REAL find/rm/mv/ps/lsof against throwaway roots, builds a unix socket with
  # python3, and shellchecks pgdr-tmp-sweep.bash. ps and lsof may be unusable in
  # a sandbox; the suite then skips the cases that need them (the sweep fails
  # closed without them), so run it outside the sandbox too.
  testDeps = [
    pkgs.jq
    pkgs.coreutils
    pkgs.findutils
    pkgs.gnugrep
    pkgs.gnused
    pkgs.lsof
    pkgs.python3
    pkgs.shellcheck
  ]
  ++ pkgs.lib.optional pkgs.stdenv.hostPlatform.isLinux pkgs.procps;
}
