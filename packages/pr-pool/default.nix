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

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./cmd
      (lib.fileset.maybeMissing ./internal)
    ];
  };

  # Stdlib-only module: no vendored dependencies.
  vendorHash = null;

  nativeBuildInputs = [ makeWrapper ];

  # pr-pool shells out to ccpool (session mechanics), bd (beads), and pg-pr
  # (config show). Wrap them onto PATH so the binary works under launchd's
  # minimal PATH.
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
