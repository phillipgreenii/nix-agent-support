{
  pkgs,
  lib,
  gitHash,
  ...
}:

pkgs.buildGoModule {
  pname = "my-code-review-support-cli";
  version = "0.0.0";
  src = ./.;

  vendorHash = "sha256-7K17JaXFsjf163g5PXCb5ng2gYdotnZ2IDKk8KFjNj0=";

  subPackages = [ "cmd/my-code-review-support-cli" ];

  ldflags = [
    "-X main.Version=${gitHash}"
  ];

  nativeBuildInputs = [ pkgs.help2man ];

  postInstall = ''
    # Generate man page
    mkdir -p $out/share/man/man1
    help2man --no-info --no-discard-stderr $out/bin/my-code-review-support-cli \
      > $out/share/man/man1/my-code-review-support-cli.1 || true

    # Install tldr page
    mkdir -p $out/share/tldr/pages.common
    cp ${./my-code-review-support-cli.md} $out/share/tldr/pages.common/my-code-review-support-cli.md

    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/my-code-review-support-cli completion bash > $out/share/bash-completion/completions/my-code-review-support-cli 2>/dev/null || true
    $out/bin/my-code-review-support-cli completion zsh > $out/share/zsh/site-functions/_my-code-review-support-cli 2>/dev/null || true
    $out/bin/my-code-review-support-cli completion fish > $out/share/fish/vendor_completions.d/my-code-review-support-cli.fish 2>/dev/null || true
  '';

  meta = with lib; {
    description = "Code review support CLI for AI agents";
    platforms = platforms.all;
  };
}
