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

  # Hard failures, no `|| true` (bead pg2-z28y9): the previous `|| true` on
  # each of these 4 generators swallowed any failure, so a cobra/help2man
  # regression could ship an empty or stale man page/completion with
  # nothing asserting non-empty output — and nothing in `nix flake check`
  # forced this package to build at all until the pg-connector-* checks
  # added alongside this fix started referencing it. Matches the
  # established convention in this repo's OTHER postInstall shell-completion
  # generation (packages/claude-extended-tool-approver/default.nix has no
  # `|| true`/`2>/dev/null` guard on its own completion generators) —
  # pg-pr/default.nix still carries the old guarded form and is out of this
  # bead's scope, but is the same latent defect.
  postInstall = ''
    # Generate man page
    mkdir -p $out/share/man/man1
    help2man --no-info --no-discard-stderr $out/bin/pg-connector \
      > $out/share/man/man1/pg-connector.1

    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/pg-connector completion bash > $out/share/bash-completion/completions/pg-connector
    $out/bin/pg-connector completion zsh > $out/share/zsh/site-functions/_pg-connector
    $out/bin/pg-connector completion fish > $out/share/fish/vendor_completions.d/pg-connector.fish
  '';

  meta = with lib; {
    description = "Unified pluggable connector umbrella CLI (pg-connector)";
    platforms = platforms.all;
  };
}
