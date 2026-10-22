{
  mkBashScript,
  coreutils,
  jq,
  git,
  beads,
  pg-rescue,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-rescue-fix-ci";
  src = ./.;
  description = "Handle one fix-ci item: claim it, request one rerun of the failed CI jobs under pg-rescue, and let the chain's give-up hand the item to the worker when the rerun is spent";
  # Deliberately NOT pinned: `bd`, `pg-connector` and `pg-rescue` (the machine's
  # own binaries and config, see ../default.nix; like sync-projects the shim runs
  # the caller's pg-rescue).
  runtimeDeps = [
    coreutils
    jq
  ];
  # The suite runs the REAL pg-rescue and the REAL bd; pg-connector is a test
  # double the suite writes itself.
  testDeps = [
    coreutils
    jq
    # bd init (the throwaway-database fixture) needs git
    git
    beads
    pg-rescue
  ];
  inherit testSupport;
}
