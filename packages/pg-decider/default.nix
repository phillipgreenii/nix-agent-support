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

  # pg-decider is the decider; pg-pr-change is the composed command a
  # `pr-change` role runs for one PR id (hydrate with `pg-desk pr refresh`, then
  # decide), kept in this module because it shares the decider's view, work-item
  # index and configuration packages. pg-pr-backfill is the PR lane's backfill
  # driver (enumerate, run over the composed command, plan summary, flip marker),
  # kept here for the same reason. Every one declares `var Version = "dev"`.
  subPackages = [
    "cmd/pg-decider"
    "cmd/pg-pr-change"
    "cmd/pg-pr-backfill"
  ];

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
    # pg-pr-change execs pg-desk, pg-decider (its sibling in this output) and
    # pg-connector by bare name. The unwrapped binary is `.pg-pr-change-wrapped`.
    wrapProgram $out/bin/pg-pr-change --prefix PATH : "${
      lib.makeBinPath [
        pg-desk
        pg-connector
      ]
    }:$out/bin"
    # pg-pr-backfill execs pg-connector (the tracker and PR listings, the epic
    # comment, the watch reads and the inspection of mark-watched), pg-desk (the
    # hydrate-only refresh of mark-watched) and pg-pr-change (its sibling in this
    # output) by bare name. The unwrapped binary is `.pg-pr-backfill-wrapped`.
    wrapProgram $out/bin/pg-pr-backfill --prefix PATH : "${
      lib.makeBinPath [
        pg-connector
        pg-desk
      ]
    }:$out/bin"
    install -Dm644 ${./share/work-item-keys.json} $out/share/pg-decider/work-item-keys.json
  '';

  meta = {
    description = "Decider that plans and applies work-item changes from the pg-desk composite view, pg-pr-change, the command that hydrates a PR and then decides it, and pg-pr-backfill, the driver that runs it over the backfill set";
    mainProgram = "pg-decider";
  };
}
