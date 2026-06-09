{
  lib,
  buildGoModule,
  fetchFromGitHub,
}:

buildGoModule rec {
  pname = "goccc";
  version = "0.3.5";

  src = fetchFromGitHub {
    owner = "backstabslash";
    repo = "goccc";
    rev = "v${version}";
    hash = "sha256-e0Phi6zWIPAC8r/KxZXoIMvecX2WooOQ+b6OyyaHAMo=";
  };

  vendorHash = null;

  meta = {
    description = "Cost calculator and customizable statusline for Claude Code";
    homepage = "https://github.com/backstabslash/goccc";
    license = lib.licenses.mit;
    mainProgram = "goccc";
  };
}
