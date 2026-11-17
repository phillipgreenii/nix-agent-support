{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-assistants";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset (mirrors
  # packages/pg-keep-awake/default.nix).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # One binary. It declares `var Version = "dev"`, the mkGoApp default ldflag
  # target (main.Version), so the per-source digest is stamped into it.
  subPackages = [ "cmd/pg-assistants" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # whole-module gate is checks.<system>.pg-assistants-go-tests in flake.nix.

  # Shell completions (bash under bash-completion, zsh under site-functions)
  # and the tldr page, flat under share/tldr/pages.common (the pg-keep-awake
  # layout; the home-manager module registers it through
  # programs.tldr.customPages).
  postInstall = ''
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/tldr/pages.common
    cp ${./completions/pg-assistants.bash} $out/share/bash-completion/completions/pg-assistants
    cp ${./completions/_pg-assistants} $out/share/zsh/site-functions/_pg-assistants
    cp ${./share/tldr/pages.common/pg-assistants.md} $out/share/tldr/pages.common/pg-assistants.md
  '';

  meta = {
    description = "pg-assistants: a minimal supervisor for permanent Claude Code assistants in tmux";
    mainProgram = "pg-assistants";
  };
}
