{
  lib,
  stdenv,
  mkGoApp,
  makeWrapper,
  gh,
  tmux,
  # pg-claude-control is the one way pa-monitor sends a nudge to a running Claude
  # (submit through the control mod's inbox); it never injects keystrokes itself.
  pg-claude-control,
  # cmux-cli is the command-line client of the macOS-only cmux .app, from
  # phillipgreenii-nix-overlay (already a declared input + applied overlay).
  # The overlay's `cmux` package is the GUI wrapper; the CLI that pa-monitor
  # runs is `cmux-cli`. Defaulted to null so the package still evaluates on
  # non-darwin, where it is omitted from PATH below.
  cmux-cli ? null,
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
      ../pg-claude-code-recorder
      ../pg-sqlite
    ];
  };
  modRoot = "pa-monitor";

  # gomod2nix engine: third-party deps are pinned in gomod2nix.toml and the
  # local `replace ../claude-transcript` resolves natively via symlink in the
  # rooted store copy (no vendorHash FOD, no localReplaceModules overlay) — ADR 0008 §"Case B".
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pa-monitor" ];

  # cmd/pa-monitor/main.go declares the version global in LOWERCASE
  # (`var version = "dev"`), but mkGoApp's default ldflag target is capital-V
  # `main.Version` (go-builders.nix, matching the fleet `var Version`
  # convention). Without this override `-X main.Version=` targets a symbol the
  # code never declares, the linker silently drops it, and every role reports
  # the "dev" fallback — which makes versioncmp.Mismatch a permanent false and
  # disables both the stale-daemon warning and the client self-restart feature.
  # Guarded by the `test-pa-monitor-version-stamped` flake check.
  versionPath = "main.version";

  nativeBuildInputs = [ makeWrapper ];

  # Wrap the binaries pa-monitor shells out to onto its PATH so they work
  # out-of-the-box under launchd (whose default PATH is /usr/bin:/bin:...):
  #   - gh: PR-status lookups
  #   - tmux / cmux-cli: signal-layer detection + auto-resume delivery. These
  #     are NOT optional — without them every session on that multiplexer is
  #     classified "unknown" and its auto-resume nudges silently fail. cmux-cli
  #     is darwin-only (it lives in the cmux .app bundle); guarded so non-darwin
  #     builds still work. Guarded by the test-pa-monitor-path-has-cmux-cli
  #     flake check.
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
            pg-claude-control
          ]
          ++ lib.optionals (stdenv.hostPlatform.isDarwin && cmux-cli != null) [ cmux-cli ]
        )
      }
  '';

  meta = {
    description = "Per-user daemon + TUI for monitoring active Claude Code sessions, context usage, billing blocks, and weekly limit. Emits OpenTelemetry metrics + events when enabled.";
    mainProgram = "pa-monitor";
  };
}
