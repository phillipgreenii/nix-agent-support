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

  # The module uses `replace ../ccpool` and `replace ../claude-transcript`, so the
  # build sandbox must contain ALL THREE package dirs at their relative positions.
  # ccpool itself replaces ../claude-transcript, so that dir must be present too
  # (it already is for pr-pool's own use) for ccpool's replace to resolve.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../ccpool
      ../claude-transcript
    ];
  };
  modRoot = "pr-pool";

  # gomod2nix engine (ADR 0008, Case B): buildGoApplication symlinks the
  # first-party local-replace modules (../ccpool, ../claude-transcript) from
  # source — live, no vendorHash, no localReplaceModules overlay. The toml tracks
  # only third-party deps; ccpool/claude-transcript are intentionally absent from it.
  gomod2nixToml = ./gomod2nix.toml;

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
