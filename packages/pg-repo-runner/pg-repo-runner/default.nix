{
  mkBashScript,
  pkgs,
  # pg-nix-log-wrapped, by absolute store path, for the nix runs the kernel
  # offers (rr_nix_run). null leaves the variable empty: rr_nix_run then runs nix
  # without the wrapper and says so (the workspace rule: never block on it).
  pgNixLogWrapped ? null,
  # bd, for the test check only (see ../default.nix).
  beads ? null,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-repo-runner";
  src = ./.;
  description = "Kernel and facade of the pg-router-managed async repo runners: resolve, admit, lease, load check, verdict store, status";
  # Runs under pg-router (launchd) with a thin PATH. runtimeDeps are only a
  # FALLBACK appended to PATH; the binaries the kernel depends on are ALSO
  # injected below by absolute store path, which is what it uses.
  runtimeDeps = [
    pkgs.coreutils
    pkgs.findutils
    pkgs.gnused
    pkgs.git
    pkgs.jq
    pkgs.tomlq
    # `ps -o lstart=` gives the lease its owner identity. A darwin build sandbox
    # refuses /bin/ps (setuid, "Operation not permitted"), so ps comes from nix.
    pkgs.ps
  ];
  config = {
    RR_GIT_BIN = "${pkgs.git}/bin/git";
    RR_JQ_BIN = "${pkgs.jq}/bin/jq";
    RR_TQ_BIN = "${pkgs.tomlq}/bin/tq";
    RR_DATE_BIN = "${pkgs.coreutils}/bin/date";
    RR_TIMEOUT_BIN = "${pkgs.coreutils}/bin/timeout";
    RR_NICE_BIN = "${pkgs.coreutils}/bin/nice";
    RR_NIX_BIN = "${pkgs.nix}/bin/nix";
    # The PATH the kernel runs with: prepended to the ambient PATH by
    # pg-repo-runner.sh, so every bare tool name (sha256sum, nproc, sort, find,
    # sed, mktemp, ps, ...) resolves to the pinned GNU one, never to whatever the
    # strict base PATH of pg-router (BSD tools only) happens to hold.
    RR_TOOLS_PATH = pkgs.lib.makeBinPath [
      pkgs.coreutils
      pkgs.findutils
      pkgs.gnused
      pkgs.ps
    ];
    RR_NIX_LOG_WRAPPED_BIN =
      if pgNixLogWrapped == null then "" else "${pgNixLogWrapped}/bin/pg-nix-log-wrapped";
  };
  testDeps = [
    pkgs.coreutils
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.git
    pkgs.jq
    pkgs.tomlq
    pkgs.ps
  ]
  ++ pkgs.lib.optional (beads != null) beads;
  inherit testSupport;
}
