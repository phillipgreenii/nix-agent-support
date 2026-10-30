{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  testSupport ? null,
}:
mkBashScript {
  name = "pg-decider-backfill-pr-metadata";
  src = ./.;
  description = "One-off backfill (bead pg2-eclw3j.5): copy repo and pr_number from the parent anchor onto decider-minted process-feedback children that lack them (dry-run by default)";
  # `bd` is deliberately not pinned here (see ../default.nix).
  runtimeDeps = [
    coreutils
    jq
  ];
  testDeps = [
    coreutils
    jq
    git
    beads
  ];
  inherit testSupport;
}
