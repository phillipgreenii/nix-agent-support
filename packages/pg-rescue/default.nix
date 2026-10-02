{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-rescue";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at
  # this package dir, with go.mod and the committed gomod2nix.toml side by
  # side. No local `replace`, so no modRoot and no parent-rooted fileset are
  # needed (mirrors packages/pg-router-probe/default.nix).
  #
  # pg-rescue wraps a command and, when it fails, walks a named chain of
  # failure handlers. It has no compile-time dependency on any other package
  # in this workspace: handlers are independent executables behind one
  # contract, found on PATH at run time.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-rescue" ];

  meta = {
    description = "Script-first command runner: wraps a command and, on failure, tries an ordered chain of named failure handlers";
    mainProgram = "pg-rescue";
  };
}
