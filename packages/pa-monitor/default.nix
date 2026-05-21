{
  lib,
  buildGoModule,
  makeWrapper,
  ccusage,
  gh,
}:

buildGoModule {
  pname = "pa-monitor";
  version = "0.1.0";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/pa-monitor" ];

  vendorHash = "sha256-CU2d10kL5XDKMe+7/LWgxrp/HorUFLwh1wbOYluJNoQ=";

  nativeBuildInputs = [ makeWrapper ];

  # Wrap `ccusage` onto the binary's PATH so the 5h billing-block header
  # works out-of-the-box without requiring the user to `npm i -g ccusage`.
  postInstall = ''
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    cp ${./completions/pa-monitor.bash} $out/share/bash-completion/completions/pa-monitor
    cp ${./completions/_pa-monitor} $out/share/zsh/site-functions/_pa-monitor

    wrapProgram $out/bin/pa-monitor \
      --prefix PATH : ${
        lib.makeBinPath [
          ccusage
          gh
        ]
      }
  '';

  meta = {
    description = "Per-user daemon + TUI for monitoring active Claude Code sessions, context usage, billing blocks, and weekly limit. Emits OpenTelemetry metrics + events when enabled.";
    mainProgram = "pa-monitor";
  };
}
