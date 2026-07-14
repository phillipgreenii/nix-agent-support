{
  mkBashScript,
  pkgs,
}:
mkBashScript {
  name = "integrate-branch-support";
  src = ./.;
  description = "Advisory: report a repo's integration facts + recommended strategy";
  runtimeDeps = [
    pkgs.git
    pkgs.jq
  ];
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
