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

  # claude-transcript is a first-party local-replace module. Keep it "live"
  # (never frozen into the vendorHash FOD) via mkGoApp's overlay — see pg2-gjzz.
  localReplaceModules = [
    {
      goImportPath = "github.com/phillipgreenii/claude-transcript";
      relPath = "../claude-transcript";
    }
  ];

  # vendorHash now captures `go mod vendor` MINUS claude-transcript (stripped from
  # the FOD by the overlay), so it tracks only third-party deps and no longer
  # drifts when claude-transcript changes.
  vendorHash = "sha256-pza6rpK2hvoVgD/IcHXaMxjijLWTlf0X8FUUT8YvEGk=";

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
