{
  lib,
  stdenv,
  buildGoModule,
  makeWrapper,
  ccusage,
  gh,
  tmux,
  # cmux is a macOS-only .app from phillipgreenii-nix-overlay (already a
  # declared input + applied overlay). Defaulted to null so the package still
  # evaluates on non-darwin, where it is omitted from PATH below.
  cmux ? null,
  version ? "dev",
}:

buildGoModule {
  pname = "pa-monitor";
  inherit version;

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/pa-monitor" ];

  vendorHash = "sha256-HMGPBpDYSUOgLwl27ugDJztae7IfAS3Y3RsfDLjUI94=";

  ldflags = [
    "-X main.version=${version}"
  ];

  nativeBuildInputs = [ makeWrapper ];

  # Wrap the binaries pa-monitor shells out to onto its PATH so they work
  # out-of-the-box under launchd (whose default PATH is /usr/bin:/bin:...):
  #   - ccusage: 5h billing-block header
  #   - gh: PR-status lookups
  #   - tmux / cmux: signal-layer detection + auto-resume delivery. These are
  #     NOT optional — without them every session on that multiplexer is
  #     classified "unknown" and its auto-resume nudges silently fail. cmux is
  #     darwin-only (a .app bundle); guarded so non-darwin builds still work.
  postInstall = ''
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    cp ${./completions/pa-monitor.bash} $out/share/bash-completion/completions/pa-monitor
    cp ${./completions/_pa-monitor} $out/share/zsh/site-functions/_pa-monitor

    wrapProgram $out/bin/pa-monitor \
      --prefix PATH : ${
        lib.makeBinPath (
          [
            ccusage
            gh
            tmux
          ]
          ++ lib.optionals (stdenv.hostPlatform.isDarwin && cmux != null) [ cmux ]
        )
      }
  '';

  meta = {
    description = "Per-user daemon + TUI for monitoring active Claude Code sessions, context usage, billing blocks, and weekly limit. Emits OpenTelemetry metrics + events when enabled.";
    mainProgram = "pa-monitor";
  };
}
