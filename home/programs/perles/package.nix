{
  buildGoModule,
  fetchFromGitHub,
  go_1_26,
}:
(buildGoModule.override { go = go_1_26; }) rec {
  pname = "perles";
  version = "0.8.4";

  src = fetchFromGitHub {
    owner = "zjrosen";
    repo = "perles";
    rev = "v${version}";
    hash = "sha256-4TImIYgFkHx+LCiik79fsY5CSXlMCvM3fT3PPLXHGds=";
  };

  vendorHash = "sha256-//5r9DsVY2A4WhdxYwoR6enfej17FMbdArU5LwESB0g=";

  subPackages = [ "." ];

  doCheck = false;

  meta = {
    description = "TUI for the beads issue tracker";
    homepage = "https://github.com/zjrosen/perles";
    mainProgram = "perles";
  };
}
