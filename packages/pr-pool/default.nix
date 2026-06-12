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

  # claude-transcript is a local-replace dep present via the fileset + modRoot
  # (mirrors ccpool). vendorHash captures `go mod vendor` over the full import
  # graph (recomputed below).
  vendorHash = "sha256-/+A6ymx3p397EAl5ifChDvw98DHDDxLDWvb7s4TPjZk=";

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
