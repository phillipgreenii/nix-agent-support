{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  testSupport ? null,
}:
mkBashScript {
  name = "handoff-create";
  src = ./.;
  description = "Create a handoff bead correctly: type, P0, title prefix, first body line, session metadata, human label policy, task fallback, read-back";
  # `bd` is deliberately not pinned here (see ../default.nix). coreutils:
  # mktemp/cat; jq: metadata JSON and the envelope-tolerant read-back.
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
