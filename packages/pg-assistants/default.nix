{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-assistants";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset (mirrors
  # packages/pg-keep-awake/default.nix).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # One binary. It declares `var Version = "dev"`, the mkGoApp default ldflag
  # target (main.Version), so the per-source digest is stamped into it.
  subPackages = [ "cmd/pg-assistants" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # whole-module gate is checks.<system>.pg-assistants-go-tests in flake.nix.

  meta = {
    description = "pg-assistants: a minimal supervisor for permanent Claude Code assistants in tmux";
    mainProgram = "pg-assistants";
  };
}
