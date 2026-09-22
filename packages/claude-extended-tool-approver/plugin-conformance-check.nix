{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "plugin-conformance-check";

  # Shares the SAME Go module as ./default.nix's cmd/claude-extended-tool-approver
  # build -- one go.mod, one gomod2nix.toml, two mkGoApp calls building two
  # different binaries out of it (mirrors packages/pg-connector's N-binaries-
  # one-module convention, e.g. pg-connector-scm-git.nix). Unlike that
  # convention's per-binary narrowed `lib.fileset` src, this binary keeps the
  # FULL module source: its own setup.NewEngineForCWD call wires up the whole
  # rule chain (internal/engine plus every internal/rules/* module), so its
  # real dependency graph is already effectively the entire module and
  # narrowing the src would buy nothing.
  src = lib.cleanSource ./.;

  subPackages = [ "cmd/plugin-conformance-check" ];

  gomod2nixToml = ./gomod2nix.toml;

  meta = {
    description = "Extracts every fenced shell command a plugin tree prescribes and asserts each resolves to a decisive ceta verdict offline (pg2-amzvw)";
    mainProgram = "plugin-conformance-check";
  };
}
