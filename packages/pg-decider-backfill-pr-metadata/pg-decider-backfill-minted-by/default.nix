{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  testSupport ? null,
}:
mkBashScript {
  name = "pg-decider-backfill-minted-by";
  src = ./.;
  description = "One-off backfill: add the minted-by:pg-decider label to the decider's existing beads that provably are its own (dry-run by default)";
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
