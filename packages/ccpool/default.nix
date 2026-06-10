{
  lib,
  buildGoModule,
  makeWrapper,
  tmux,
  version ? "dev",
}:

buildGoModule {
  pname = "ccpool";
  inherit version;

  # The module uses a relative `replace ../claude-transcript`, so the build
  # sandbox must contain BOTH package dirs at their relative positions. Root the
  # source at packages/ and build the ccpool subdir (spec §5.1, §14).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../claude-transcript
    ];
  };
  modRoot = "ccpool";

  vendorHash = "sha256-dP7Bg2smH6Z0BESIKwNJexltGfSkkAqtJx61/D5/g04=";

  ldflags = [ "-X main.version=${version}" ];

  nativeBuildInputs = [ makeWrapper ];

  # Render the hook plugin with an ABSOLUTE binary path (the repo's template uses
  # `ccpool hook <event>`; substitute the store path). Wrap tmux onto PATH so the
  # binary works under launchd's minimal PATH.
  postInstall = ''
    mkdir -p $out/share/ccpool-plugin/.claude-plugin $out/share/ccpool-plugin/hooks
    cp ${./ccpool-plugin/.claude-plugin/plugin.json} $out/share/ccpool-plugin/.claude-plugin/plugin.json
    sed 's#"command": "ccpool #"command": "'"$out"'/bin/ccpool #g' \
      ${./ccpool-plugin/hooks/hooks.json} > $out/share/ccpool-plugin/hooks/hooks.json

    wrapProgram $out/bin/ccpool --prefix PATH : ${lib.makeBinPath [ tmux ]}
  '';

  meta = {
    description = "Claude Code session pool manager";
    mainProgram = "ccpool";
  };
}
