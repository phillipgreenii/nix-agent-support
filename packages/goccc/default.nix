{
  lib,
  buildGoModule,
  fetchFromGitHub,
}:

buildGoModule rec {
  pname = "goccc";
  version = "0.3.3";

  src = fetchFromGitHub {
    owner = "backstabslash";
    repo = "goccc";
    rev = "v${version}";
    hash = "sha256-LslKOaSn12b+Nk7GZCv3n23Eu8RITkcSG2PwOMk8j+g=";
  };

  vendorHash = null;

  # Upstream go.mod requires Go 1.26; pinned nixpkgs ships 1.25. goccc uses
  # only stdlib (no 1.26-specific features), so relaxing the directive is
  # safe. Drop once nixpkgs catches up.
  postPatch = ''
    substituteInPlace go.mod --replace-fail "go 1.26.0" "go 1.25"
  '';

  meta = {
    description = "Cost calculator and customizable statusline for Claude Code";
    homepage = "https://github.com/backstabslash/goccc";
    license = lib.licenses.mit;
    mainProgram = "goccc";
  };
}
