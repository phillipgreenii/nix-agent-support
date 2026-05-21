{
  lib,
  rustPlatform,
  fetchFromGitHub,
}:

rustPlatform.buildRustPackage rec {
  pname = "toktrack";
  version = "2.7.4";

  src = fetchFromGitHub {
    owner = "mag123c";
    repo = "toktrack";
    rev = "v${version}";
    hash = "sha256-euiPGSJpDAb9QYYH1h5dK1BhemgMxc0mkC9h5Cv0OTI=";
  };

  cargoHash = "sha256-cgLMnkc5N57jg2LeKdkVfHeCGknASGfAn4RQ6KbHY8Q=";

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
