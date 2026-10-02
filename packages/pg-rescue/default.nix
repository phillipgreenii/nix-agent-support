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

  # Each reference handler is its own binary (design: one package, several
  # cmd/). Handlers are found on PATH at run time, so they ship alongside the
  # wrapper but never link against it. pg-rescue-claude is the reference
  # claude -p failure handler (it execs `claude` from PATH at run time, so it
  # has no build-time dependency on it).
  subPackages = [
    "cmd/pg-rescue"
    "cmd/pg-rescue-notify"
    "cmd/pg-rescue-bead"
    "cmd/pg-rescue-claude"
  ];

  # The tldr page is a committed pg-rescue.md copied into place the way
  # packages/pg-pr does it; a home-manager module (a later bead) wires it
  # into programs.tldr.customPages from this path.
  postInstall = ''
    mkdir -p $out/share/tldr/pages.common
    cp ${./pg-rescue.md} $out/share/tldr/pages.common/pg-rescue.md
  '';

  meta = {
    description = "Script-first command runner: wraps a command and, on failure, tries an ordered chain of named failure handlers";
    mainProgram = "pg-rescue";
  };
}
