{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-task-focus";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset (mirrors
  # packages/pg-rescue/default.nix). The daemon talks to no other package of
  # this workspace: the connector backend (a later sub-project) reaches it over
  # its HTTP API, never by linking it.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # One binary: `pg-task-focus serve` is the daemon, every other verb its
  # client. The package version is the per-source digest mkGoApp stamps into
  # main.Version (cmd/pg-task-focus/main.go declares `var Version = "dev"`,
  # mkGoApp's default ldflag target), so `--version` and /healthz report it.
  subPackages = [ "cmd/pg-task-focus" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false, bead
  # pg2-pla9d.2): the whole-module gate is checks.<system>.pg-task-focus-go-tests
  # in flake.nix.

  meta = {
    description = "pg-task-focus: a local daemon and CLI that keep a written daily routine (checklists and timed work cycles) as an append-only event log";
    mainProgram = "pg-task-focus";
  };
}
