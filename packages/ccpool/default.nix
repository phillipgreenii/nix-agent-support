{
  pkgs,
  lib,
  mkGoApp,
  makeWrapper,
  tmux,
}:

mkGoApp {
  pname = "ccpool";

  # The module uses a relative `replace ../claude-transcript`, so the build
  # sandbox must contain BOTH package dirs at their relative positions. Root the
  # source at packages/ and build the ccpool subdir — the rooted-fileset +
  # modRoot form (phillipg-nix-repo-base ADR 0008, Pattern B).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../claude-transcript
    ];
  };
  modRoot = "ccpool";

  # gomod2nix engine (ADR 0008, Case B): buildGoApplication symlinks the
  # first-party local-replace module (../claude-transcript) from source — live,
  # no vendorHash, no localReplaceModules overlay. The toml tracks only
  # third-party deps; claude-transcript is intentionally absent from it.
  gomod2nixToml = ./gomod2nix.toml;

  nativeBuildInputs = [ makeWrapper ];

  # internal/gitfacet's tests (pg2-svfbb.8, migrated onto x/gitclient's
  # gittest/gitfixture) build real throwaway repos via the `git` binary rather
  # than skipping when it is absent (the pre-migration tests used a
  # gitAvailable(t) skip so the doCheck phase degraded gracefully in a
  # git-less sandbox). gomod2nix's buildGoApplication runs doCheck (`go test
  # ./...`) with no subPackages scoping here, so git must be on PATH during
  # the build sandbox's check phase — mirrors pg-pr's own
  # nativeCheckInputs = [ pkgs.git ] (packages/pg-pr/default.nix).
  nativeCheckInputs = [ pkgs.git ];

  # Render the hook plugin with an ABSOLUTE binary path (the repo's template uses
  # `ccpool hook <event>`; substitute the store path). Wrap tmux onto PATH so the
  # binary works under launchd's minimal PATH.
  postInstall = ''
    mkdir -p $out/share/ccpool-plugin/.claude-plugin $out/share/ccpool-plugin/hooks
    cp ${./ccpool-plugin/.claude-plugin/plugin.json} $out/share/ccpool-plugin/.claude-plugin/plugin.json
    sed 's#"command": "ccpool #"command": "'"$out"'/bin/ccpool #g' \
      ${./ccpool-plugin/hooks/hooks.json} > $out/share/ccpool-plugin/hooks/hooks.json

    # Static shell completions for the top-level subcommand set (pg2-htmkq).
    # ccpool is a hand-rolled flag.FlagSet dispatcher, not cobra, so unlike
    # pg-pr's `completion <shell>`-generated files these are committed source
    # under ./completions, kept in sync by hand with cmd/ccpool/dispatch.go's
    # subcommand registry. Installed at the same standard locations pg-pr
    # uses, which home-manager's zsh/bash completion machinery already
    # scans for every package on PATH -- no extra wiring needed.
    mkdir -p $out/share/zsh/site-functions $out/share/bash-completion/completions
    cp ${./completions/_ccpool} $out/share/zsh/site-functions/_ccpool
    cp ${./completions/ccpool.bash} $out/share/bash-completion/completions/ccpool

    wrapProgram $out/bin/ccpool --prefix PATH : ${lib.makeBinPath [ tmux ]}
  '';

  meta = {
    description = "Claude Code session pool manager";
    mainProgram = "ccpool";
  };
}
