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
  version = "1.2.1";

  src = fetchFromGitHub {
    owner = "gastownhall";
    repo = "gascity";
    rev = "v${version}";
    hash = "sha256-q9ehkxbkq4bnGn8vB0OM/8MJRk6zgVCBLnlrmHx7/RI=";
  };

  vendorHash = "sha256-jKuPfAilxCndnkOCJf475wLh0DyxZxXQ33c+7nwFYzM=";

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
