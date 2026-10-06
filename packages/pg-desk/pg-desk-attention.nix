{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-desk-attention";

  # Shares the SAME Go module as ./default.nix's cmd/pg-desk build — one
  # go.mod, one gomod2nix.toml, two mkGoApp calls building two binaries out
  # of it (the per-binary shape of packages/pg-connector/pg-connector-*.nix).
  # It is NOT a pg-connector backend: it lives in pg-desk and imports
  # pg-connector's wire packages, so the dependency direction stays pg-desk to
  # pg-connector (ADR 0077); the umbrella learns of the plugin only as a bare
  # name in its attention.sources registry.
  #
  # Pattern B source (phillipg-nix-repo-base ADR 0008), as ./default.nix: the
  # go.mod `replace => ../pg-connector` needs both package dirs at their
  # relative positions. Filtered to what this binary's build graph reaches
  # (verified with `go list -deps ./cmd/pg-desk-attention`): pg-desk's module
  # files, cmd/pg-desk-attention and internal/**, plus the whole local-replace
  # module (a replaced module is resolved from its directory, and its own
  # go.mod must be present). Editing cmd/pg-desk therefore does not touch this
  # derivation's content hash.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./gomod2nix.toml
      ./internal
      ./cmd/pg-desk-attention
      ../pg-connector
    ];
  };
  modRoot = "pg-desk";

  # gomod2nix engine (ADR 0008, Case B): buildGoApplication symlinks the
  # local-replace module (../pg-connector) from source; the toml tracks only
  # third-party deps.
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-desk-attention" ];

  # Exports its version as `main.Version` (capitalised), mkGoApp's default.
  versionPath = "main.Version";

  # No wrapProgram and no PATH dependencies: the plugin execs nothing (it
  # reads pg-desk's local store only), so unlike ./default.nix it is NOT
  # wrapped with pg-connector. No man page or completions either: it speaks
  # only the scriptout wire protocol and has no CLI a person types.

  meta = {
    description = "pg-desk's attention plugin: a scriptout-only list_attention backend over pg-desk's local store, with no independent CLI identity";
    mainProgram = "pg-desk-attention";
    platforms = lib.platforms.all;
  };
}
