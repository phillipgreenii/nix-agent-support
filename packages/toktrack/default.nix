{
  lib,
  rustPlatform,
  fetchFromGitHub,
}:

rustPlatform.buildRustPackage rec {
  pname = "toktrack";
  version = "2.9.0";

  src = fetchFromGitHub {
    owner = "mag123c";
    repo = "toktrack";
    rev = "v${version}";
    hash = "sha256-1yxdDxxzLDWDumHUvQmr2w6gMtBeSyFXb+jxuGNiXiE=";
  };

  cargoHash = "sha256-dz0A0cfJoIn7EnvgZi/WYpM6slNNCg58qJqKTqOAyzY=";

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
