{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-rescue";

  # Pattern B (phillipg-nix-repo-base ADR 0008): go.mod replaces ../pg-connector
  # for its published client package (the bead handler decodes pg-connector's
  # answers with it), so the build sandbox holds this module and that one at
  # their relative positions.
  #
  # pg-rescue wraps a command and, when it fails, walks a named chain of
  # failure handlers. Handlers are independent executables behind one
  # contract, found on PATH at run time; the bead handler still execs
  # pg-connector from PATH, using the client package for its result types.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-connector
    ];
  };
  modRoot = "pg-rescue";

  # go.work is for the editor and go-work-vet only; a build resolves through go.mod.
  env.GOWORK = "off";

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
    "cmd/pg-rescue-triage"
  ];

  # The tldr page is a committed pg-rescue.md copied into place the way
  # the retired PR tool does it; a home-manager module (a later bead) wires it
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
