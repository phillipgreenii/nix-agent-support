{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-ccaudit";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. There
  # is deliberately NO local `replace => ../claude-transcript`, so no modRoot and
  # no parent-rooted fileset are needed.
  #
  # claude-transcript was evaluated and NOT reused. Its Event/Block types carry
  # only the fields ccpool/pa-monitor/pr-pool need, and omit every field this
  # index is about — the tool_use `input`, the tool_result `content` and
  # `is_error`, `parentUuid`, `isSidechain`, `cwd`, `gitBranch`, `durationMs`.
  # Adding them would change a type three other packages depend on, and its reader
  # is a whole-file scan with no byte-offset resume, which is the one thing T-1a
  # makes mandatory here. Extending it would have meant a wider blast radius for
  # a smaller result.
  src = lib.cleanSource ./.;
  modRoot = null;

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
