{
  lib,
  buildGoModule,
  makeWrapper,
  ccusage,
  gh,
}:

buildGoModule {
  pname = "claude-agents-tui";
  version = "0.1.0";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/claude-agents-tui" ];

  vendorHash = "sha256-JDnqDtGoXuPIE0eAlcPcYohD5U9JMI9eSaTdWVv9yio=";

  nativeBuildInputs = [ makeWrapper ];

  # Wrap `ccusage` onto the binary's PATH so the 5h billing-block header
  # works out-of-the-box without requiring the user to `npm i -g ccusage`.
  # See packages/ccusage/README.md for why the dep is packaged separately
  # instead of being vendored into this derivation.
  postInstall = ''
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    cp ${./completions/claude-agents-tui.bash} $out/share/bash-completion/completions/claude-agents-tui
    cp ${./completions/_claude-agents-tui} $out/share/zsh/site-functions/_claude-agents-tui

    wrapProgram $out/bin/claude-agents-tui \
      --prefix PATH : ${
        lib.makeBinPath [
          ccusage
          gh
        ]
      }
  '';

  meta = {
    description = "TUI for monitoring active Claude Code sessions, context usage, and billing-block burn";
    mainProgram = "claude-agents-tui";
  };
}
