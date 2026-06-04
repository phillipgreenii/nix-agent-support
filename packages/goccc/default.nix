{
  lib,
  buildGoModule,
  fetchFromGitHub,
}:

buildGoModule rec {
  pname = "goccc";
  version = "0.3.4";

  src = fetchFromGitHub {
    owner = "backstabslash";
    repo = "goccc";
    rev = "v${version}";
    hash = "sha256-xjzeRwrCDZylq+xK6m4Pnul0ODdaMm4qHxOmuqN5/fw=";
  };

  vendorHash = null;

  meta = {
    description = "Cost calculator and customizable statusline for Claude Code";
    homepage = "https://github.com/backstabslash/goccc";
    license = lib.licenses.mit;
    mainProgram = "goccc";
  };
}
