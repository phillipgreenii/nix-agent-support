{
  lib,
  rustPlatform,
  fetchFromGitHub,
}:

rustPlatform.buildRustPackage rec {
  pname = "toktrack";
  version = "2.13.1";

  src = fetchFromGitHub {
    owner = "mag123c";
    repo = "toktrack";
    rev = "v${version}";
    hash = "sha256-ZTddIRcwPtuPjLmJOzInO9WRMdLeK88I5kidBMqbO5c=";
  };

  cargoHash = "sha256-xaJJidPgal2gC49PqGbcnTtUVOVHiyZ6A0w+osWgr8k=";

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
