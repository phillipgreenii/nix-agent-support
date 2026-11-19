{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-assistants";

  # The module resolves the recorder client (the quota meter's usage source)
  # through a relative `replace ../pg-claude-code-recorder` (and that module's
  # `replace ../pg-sqlite`), so the build sandbox must contain those package
  # dirs at their relative positions: root the source at packages/ and build
  # the pg-assistants subdir (the same shape as pa-monitor/default.nix).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-claude-code-recorder
      ../pg-sqlite
    ];
  };
  modRoot = "pg-assistants";

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
