{
  lib,
  rustPlatform,
  fetchFromGitHub,
}:

rustPlatform.buildRustPackage rec {
  pname = "toktrack";
  version = "2.8.0";

  src = fetchFromGitHub {
    owner = "mag123c";
    repo = "toktrack";
    rev = "v${version}";
    hash = "sha256-hhPNajfYUTnB2Y+p4H5kn0ZOTtIthbylzQ93jcsY9uM=";
  };

  cargoHash = "sha256-Ys+/LPjU8pv+mAQuYDGsk70VfeeLDnoR552/aULp1W8=";

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
