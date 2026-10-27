{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-probe";

  # Pattern A (phillipg-nix-repo-base ADR 0008): one standalone module at this
  # package dir, stdlib only, so there is no local `replace` and no modRoot.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-probe" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false, bead
  # pg2-pla9d.2): the whole-module gate is checks.<system>.pg-probe-go-tests in
  # flake.nix.

  # Deliberately NO wrapProgram / PATH prefix: pg-probe shells out to nothing,
  # and must not put any other tool on the PATH of a session that holds it.

  # The tldr page, flat under share/tldr/pages.common (the layout pg-keep-awake
  # ships; the home module registers it through programs.tldr.customPages).
  postInstall = ''
    mkdir -p $out/share/tldr/pages.common
    cp ${./share/tldr/pages.common/pg-probe.md} $out/share/tldr/pages.common/pg-probe.md
  '';

  meta = {
    description = "pg-probe: read-only pprof-socket GETs (fixed allowlist) and a wait-log replacement for tail -f | grep -m1";
    mainProgram = "pg-probe";
  };
}
