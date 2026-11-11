{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-git-lander";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # `var Version = "dev"` in cmd/pg-git-lander is the mkGoApp default ldflag
  # target (main.Version). This build is NOT the Go test gate: that is
  # checks.<system>.pg-git-lander-go-tests in flake.nix.
  subPackages = [ "cmd/pg-git-lander" ];

  postInstall = ''
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/tldr/pages.common
    cp ${./completions/pg-git-lander.bash} $out/share/bash-completion/completions/pg-git-lander
    cp ${./completions/_pg-git-lander} $out/share/zsh/site-functions/_pg-git-lander
    cp ${./share/tldr/pages.common/pg-git-lander.md} $out/share/tldr/pages.common/pg-git-lander.md
  '';

  meta = {
    description = "pg-git-lander: lock-safe rebase and land tool";
    mainProgram = "pg-git-lander";
  };
}
