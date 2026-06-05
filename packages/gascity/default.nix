{
  lib,
  buildGoModule,
  fetchFromGitHub,
  go_1_25,
  makeWrapper,
  tmux,
  jq,
  git,
  dolt,
  beads,
  flock,
}:

buildGoModule rec {
  pname = "gascity";
  version = "1.1.0";

  src = fetchFromGitHub {
    owner = "gastownhall";
    repo = "gascity";
    rev = "v${version}";
    hash = "sha256-1W4bKBcRcEd5MBKCQr005EY/veZu/Co2G5pL2WE7Nmk=";
  };

  vendorHash = "sha256-d1esYYBayZ6oFFGC+5/ufa0n8XXrZX5cZa0Lns+NB7s=";

  go = go_1_25;

  subPackages = [ "cmd/gc" ];

  # Tests require tmux, dolt, git, working ports inside the build sandbox; skip them.
  # `doCheck = false` alone wasn't honored when the flake builds via apply (likely a
  # different evaluation path setting it back to 1). Override checkPhase to a no-op
  # so tests cannot run regardless of doCheck propagation.
  doCheck = false;
  checkPhase = "true";
  installCheckPhase = "true";

  nativeBuildInputs = [ makeWrapper ];

  # `beads` (bd) is bundled, not just dolt: the supervisor runs under launchd
  # with a minimal PATH that does NOT include the user profile where bd lives,
  # so without this the supervisor's beads-lifecycle init fails with
  # `bd: command not found`. Bundling makes gc self-sufficient regardless of
  # the install-time/launchd PATH (same rationale as dolt).
  postFixup = ''
    wrapProgram $out/bin/gc \
      --prefix PATH : ${
        lib.makeBinPath [
          tmux
          jq
          git
          dolt
          beads
          flock
        ]
      }
  '';

  meta = with lib; {
    description = "Gas City — orchestration-builder SDK for multi-agent coding workflows";
    homepage = "https://github.com/gastownhall/gascity";
    license = licenses.mit;
    mainProgram = "gc";
    platforms = platforms.unix;
  };
}
