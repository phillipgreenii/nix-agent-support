{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-ccaudit";

  # Pattern B (phillipg-nix-repo-base ADR 0008): the module has one local
  # `replace => ../pg-claude-code-recorder` (the recorder's Go client, which the
  # ingest reads transcripts through), so the build sandbox must hold both
  # package dirs at their relative positions: root the source at packages/ and
  # build the pg-ccaudit subdir (rooted fileset + modRoot).
  #
  # claude-transcript was evaluated and NOT reused. Its Event/Block types carry
  # only the fields ccpool/pa-monitor/pg-router need, and omit every field this
  # index is about — the tool_use `input`, the tool_result `content` and
  # `is_error`, `parentUuid`, `isSidechain`, `cwd`, `gitBranch`, `durationMs`.
  # Adding them would change a type three other packages depend on, and its reader
  # is a whole-file scan with no byte-offset resume, which is the one thing T-1a
  # makes mandatory here. Extending it would have meant a wider blast radius for
  # a smaller result.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-claude-code-recorder
    ];
  };
  modRoot = "pg-ccaudit";

  gomod2nixToml = ./gomod2nix.toml;

  # Pin the shipped binary to the single entrypoint. The full `go test ./...`
  # suite is gated separately by the flake's pg-ccaudit-go-tests check, which is
  # the builder that deliberately does NOT set subPackages.
  subPackages = [ "cmd/pg-ccaudit" ];

  meta = {
    description = "Index Claude Code transcripts into SQLite and query them with named, versioned canned queries";
    mainProgram = "pg-ccaudit";
  };
}
