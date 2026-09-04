{
  pkgs,
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector";
  src = ./.;

  # gomod2nix engine (ADR 0008, Case A): no local replace, so third-party
  # deps are pinned in gomod2nix.toml and buildGoApplication builds from
  # src — no vendorHash FOD.
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring pg-pr/default.nix.
  versionPath = "main.Version";

  nativeBuildInputs = [ pkgs.help2man ];

  postInstall = ''
    # Generate man page
    mkdir -p $out/share/man/man1
    help2man --no-info --no-discard-stderr $out/bin/pg-connector \
      > $out/share/man/man1/pg-connector.1 || true

    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/pg-connector completion bash > $out/share/bash-completion/completions/pg-connector 2>/dev/null || true
    $out/bin/pg-connector completion zsh > $out/share/zsh/site-functions/_pg-connector 2>/dev/null || true
    $out/bin/pg-connector completion fish > $out/share/fish/vendor_completions.d/pg-connector.fish 2>/dev/null || true
  '';

  meta = with lib; {
    description = "Unified pluggable connector umbrella CLI (pg-connector)";
    platforms = platforms.all;
  };
}
