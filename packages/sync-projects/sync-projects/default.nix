{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "sync-projects";
  src = ./.;
  description = "Rebase every workspace repo onto its origin under pg-rescue, then pn workspace push";
  # Deliberately NO runtimeDeps. The script calls only `pg-rescue` and `pn`,
  # and both MUST be the caller's own: `pg-rescue` reads the caller's
  # config.toml and handler chain, and `pn` is the workspace tool installed
  # alongside it. The `git` that `pg-rescue` runs (`git pull --rebase`) is
  # likewise the caller's, so the user's git config, credential helpers and ssh
  # setup apply exactly as they do for a hand-typed `git pull`.
  runtimeDeps = [ ];
  # The suite fakes pg-rescue, git and pn; it needs only coreutils-grade tools.
  testDeps = [
    pkgs.coreutils
  ];
}
