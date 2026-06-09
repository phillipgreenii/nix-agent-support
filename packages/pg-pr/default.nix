{
  pkgs,
  lib,
  version ? "dev",
  ...
}:

pkgs.buildGoModule {
  pname = "pg-pr";
  inherit version;
  src = ./.;

  vendorHash = "sha256-6qpEixCKR3JZF8zeRxXsd2wBWZ+ASIIeRJEq/U1V43w=";

  subPackages = [ "cmd/pg-pr" ];

  ldflags = [
    "-X main.Version=${version}"
  ];

  nativeBuildInputs = [ pkgs.help2man ];

  nativeCheckInputs = [ pkgs.git ];

  postInstall = ''
    # Generate man page
    mkdir -p $out/share/man/man1
    help2man --no-info --no-discard-stderr $out/bin/pg-pr \
      > $out/share/man/man1/pg-pr.1 || true

    # Install tldr page
    mkdir -p $out/share/tldr/pages.common
    cp ${./pg-pr.md} $out/share/tldr/pages.common/pg-pr.md

    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/pg-pr completion bash > $out/share/bash-completion/completions/pg-pr 2>/dev/null || true
    $out/bin/pg-pr completion zsh > $out/share/zsh/site-functions/_pg-pr 2>/dev/null || true
    $out/bin/pg-pr completion fish > $out/share/fish/vendor_completions.d/pg-pr.fish 2>/dev/null || true
  '';

  meta = with lib; {
    description = "Unified PR-work CLI for agents and humans";
    platforms = platforms.all;
  };
}
