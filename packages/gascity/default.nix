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
  doCheck = false;
  checkPhase = "true";
  installCheckPhase = "true";

  nativeBuildInputs = [ makeWrapper ];

  postFixup = ''
    wrapProgram $out/bin/gc \
      --prefix PATH : ${
        lib.makeBinPath [
          tmux
          jq
          git
          dolt
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
