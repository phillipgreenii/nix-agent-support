{
  mkBashScript,
  coreutils,
  jq,
  git,
  lsof,
  gawk,
  beads,
  wtdone,
  wt-liveness-lib,
  testSupport ? null,
}:
mkBashScript {
  name = "wtaudit";
  src = ./.;
  description = "Deterministic classifier and remover for worktrees and branches: scan prints a verdict per candidate as JSON, remove re-verifies and deletes with a compare-and-swap";
  libraries = [ wt-liveness-lib ];
  # The one tool whose identity matters is pinned through config. The builder
  # APPENDS runtimeDeps to PATH, so an ambient git/jq/lsof wins over these; they
  # are a FALLBACK so the command works from a minimal environment, not a pin.
  # bd, pn and pnwf are deliberately not here (ambient machine PATH).
  config = {
    WTDONE_BIN = "${wtdone}/bin/wtdone";
  };
  runtimeDeps = [
    git
    jq
    lsof
    coreutils
    gawk
  ];
  testDeps = [
    git
    jq
    lsof
    coreutils
    gawk
    beads
  ];
  inherit testSupport;
}
