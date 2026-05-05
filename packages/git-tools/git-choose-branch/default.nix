{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "git-choose-branch";
  src = ./.;
  description = "Interactive branch selector using fzf with git log preview";
  runtimeDeps = [
    pkgs.git
    pkgs.fzf
  ];
  testDeps = [ pkgs.git ];
}
