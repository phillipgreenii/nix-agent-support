{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  testSupport ? null,
}:
mkBashScript {
  name = "hand-to-human";
  src = ./.;
  description = "Hand a bead to the operator: write the strict label pair human + attention and remove escalated in ONE update call, with a read-back";
  # `bd` (and `pg-connector` for --backend) are deliberately not pinned here
  # (see ../default.nix): both resolve from the ambient machine PATH.
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
