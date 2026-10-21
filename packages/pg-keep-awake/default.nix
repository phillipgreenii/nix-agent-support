{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-keep-awake";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset (mirrors
  # packages/pg-task-focus/default.nix). internal/otel and internal/core/power
  # in pa-monitor cannot be imported across modules; the packets that need
  # that code copy it in with a note naming the origin.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # Two binaries: pg-keep-awake is the keeper and its CLI; pg-caffeinate-shim
  # stands in for /usr/bin/caffeinate. Both declare `var Version = "dev"`, the
  # mkGoApp default ldflag target (main.Version), so the per-source digest is
  # stamped into both.
  subPackages = [
    "cmd/pg-keep-awake"
    "cmd/pg-caffeinate-shim"
  ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false, bead
  # pg2-pla9d.2): the whole-module gate is checks.<system>.pg-keep-awake-go-tests
  # in flake.nix.

  # Shell completions (the pa-monitor layout: bash under bash-completion,
  # zsh under site-functions) and the tldr page, flat under
  # share/tldr/pages.common (the layout wtdone, the retired PR tool and pg-desk ship; the
  # home-manager shim wiring registers it through programs.tldr.customPages).
  postInstall = ''
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/tldr/pages.common
    cp ${./completions/pg-keep-awake.bash} $out/share/bash-completion/completions/pg-keep-awake
    cp ${./completions/_pg-keep-awake} $out/share/zsh/site-functions/_pg-keep-awake
    cp ${./share/tldr/pages.common/pg-keep-awake.md} $out/share/tldr/pages.common/pg-keep-awake.md
  '';

  meta = {
    description = "pg-keep-awake: the single holder of the Mac keep-awake assertion, with a caffeinate-compatible shim";
    mainProgram = "pg-keep-awake";
  };
}
