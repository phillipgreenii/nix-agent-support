{
  lib,
  mkGoApp,
  makeWrapper,
  ccpool,
  bd,
  pg-pr,
}:

mkGoApp {
  pname = "pr-pool";

  # The module uses `replace ../claude-transcript`, so the build sandbox must
  # contain BOTH package dirs at their relative positions (mirror ccpool).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../claude-transcript
    ];
  };
  modRoot = "pr-pool";

  # claude-transcript is itself stdlib-only; vendorHash MAY stay null. Determine
  # empirically in Step 4 — if the build complains, set the printed hash.
  vendorHash = "sha256-pCQ2i7Q5VEer9HmTUX30m9Rd4bvZ0D+vPawI/PrkqsM=";

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    wrapProgram $out/bin/pr-pool --prefix PATH : ${
      lib.makeBinPath [
        ccpool
        bd
        pg-pr
      ]
    }
  '';

  meta = {
    description = "PR-pool orchestrator (delegates claude+tmux to ccpool)";
    mainProgram = "pr-pool";
  };
}
