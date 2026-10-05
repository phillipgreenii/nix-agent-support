{
  lib,
  mkGoApp,
  makeWrapper,
  pg-desk,
  pg-connector,
}:

mkGoApp {
  pname = "pg-decider";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed (mirrors packages/pg-router-source-pg-desk/default.nix).
  #
  # This binary has no compile-time dependency on packages/pg-desk or
  # packages/pg-connector: it execs them as subprocesses and parses their
  # stdout JSON generically.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-decider" ];

  nativeBuildInputs = [ makeWrapper ];

  # Supplies pg-desk and pg-connector on PATH, resolved at runtime by bare
  # name. The unwrapped binary is left behind as `.pg-decider-wrapped`.
  #
  # The golden work-item key list is shipped at a stable path so Phase 9's
  # deployment-repo check can read it through its flake input.
  postInstall = ''
    wrapProgram $out/bin/pg-decider --prefix PATH : ${
      lib.makeBinPath [
        pg-desk
        pg-connector
      ]
    }
    install -Dm644 ${./share/work-item-keys.json} $out/share/pg-decider/work-item-keys.json
  '';

  meta = {
    description = "Decider that plans and applies work-item changes from the pg-desk composite view";
    mainProgram = "pg-decider";
  };
}
