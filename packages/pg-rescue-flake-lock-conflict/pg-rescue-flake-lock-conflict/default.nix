{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-rescue-flake-lock-conflict";
  src = ./.;
  # Internal: run by pg-rescue by name (config `command = [...]`), never typed
  # by a person, so it ships no tldr page or shell completions. The home-manager
  # module (a later bead) installs `.script` for the chain to find on PATH.
  public = false;
  description = "pg-rescue handler: resolve a rebase that stopped only on a flake.lock conflict by taking upstream's lock and relocking the conflicted inputs";
  # Every tool the script invokes, declared rather than assumed on the caller's
  # PATH (the handler runs under launchd / ccpool / pg-router with a thin
  # PATH). `nix` is deliberately NOT here: the relock MUST use the caller's own
  # nix (its config, its registry, its credentials), and it is resolved from
  # PATH at run time. `pg-rescue` (the `result` helper) is likewise the
  # caller's: the wrapper that started this handler is on PATH by definition.
  runtimeDeps = [
    pkgs.git
    pkgs.gawk
    pkgs.gnugrep
    pkgs.gnused
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.git
    pkgs.gawk
    pkgs.gnugrep
    pkgs.gnused
    pkgs.coreutils
  ];
  inherit testSupport;
}
