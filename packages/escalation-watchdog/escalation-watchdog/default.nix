{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  hand-to-human,
  testSupport ? null,
}:
mkBashScript {
  name = "escalation-watchdog";
  src = ./.;
  description = "Hand beads that stayed escalated too long to the operator, marked stale-escalation (the Dead Letter hand-off of the escalation flow)";
  # `bd` and `hand-to-human` are deliberately not pinned here (see ../default.nix):
  # both resolve from the ambient machine PATH.
  runtimeDeps = [
    coreutils
    jq
  ];
  testDeps = [
    coreutils
    jq
    git
    beads
    hand-to-human
  ];
  inherit testSupport;
}
