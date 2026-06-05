{
  lib,
  rustPlatform,
  fetchFromGitHub,
}:

rustPlatform.buildRustPackage rec {
  pname = "toktrack";
  version = "2.8.1";

  src = fetchFromGitHub {
    owner = "mag123c";
    repo = "toktrack";
    rev = "v${version}";
    hash = "sha256-GlY+fHJlijf9nZTB6mslfOjU5w3iq1KiWWTvS1sN8GE=";
  };

  cargoHash = "sha256-x4XUXoF0LSC1GzpS4I3sxCIMP2Klf5ORAvDZhUPHLC0=";

  # rusqlite uses the `bundled` feature (vendored C SQLite), and reqwest uses
  # `rustls-tls` (no openssl), so no system libs or darwin frameworks needed.
  # Tests hit the filesystem and network; skip in the sandbox.
  doCheck = false;

  meta = {
    description = "Ultra-fast token & cost tracker for Claude Code, Codex CLI, and Gemini CLI";
    homepage = "https://github.com/mag123c/toktrack";
    license = lib.licenses.mit;
    mainProgram = "toktrack";
  };
}
