{
  lib,
  stdenv,
  mkGoApp,
  makeWrapper,
  gh,
  tmux,
  # cmux is a macOS-only .app from phillipgreenii-nix-overlay (already a
  # declared input + applied overlay). Defaulted to null so the package still
  # evaluates on non-darwin, where it is omitted from PATH below.
  cmux ? null,
}:

mkGoApp {
  pname = "pa-monitor";

  # The module uses a relative `replace ../claude-transcript` (the Claude
  # transcript event model is shared with ccpool), so the build sandbox must
  # contain BOTH package dirs at their relative positions. Root the source at
  # packages/ and build the pa-monitor subdir.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../claude-transcript
    ];
  };
  modRoot = "pa-monitor";

  # gomod2nix engine: third-party deps are pinned in gomod2nix.toml and the
  # local `replace ../claude-transcript` resolves natively via symlink in the
  # rooted store copy (no vendorHash FOD, no localReplaceModules overlay) — ADR 0008 §"Case B".
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pa-monitor" ];

  # Binary version (`-X main.version`) is derived from this package's own
  # source by mkGoApp; `main.version` (lowercase) is mkGoApp's default target.

  nativeBuildInputs = [ makeWrapper ];

  # Wrap the binaries pa-monitor shells out to onto its PATH so they work
  # out-of-the-box under launchd (whose default PATH is /usr/bin:/bin:...):
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
