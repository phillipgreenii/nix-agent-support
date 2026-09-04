{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-ci-github-actions";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build —
  # one go.mod, one gomod2nix.toml, N mkGoApp calls building N different
  # binaries out of it (this is the ci capability's Tier-2 GitHub Actions
  # backend, sibling to pg-connector-pr-github.nix); this packet does not
  # create a second Go module [design: §5.2, §5.3].
  src = ./.;
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-ci-github-actions" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring pg-connector-pr-github.nix's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (unlike default.nix's cmd/pg-connector
  # build): this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # [design: §5 preamble; freedom boundary, part 4].
  #
  # No wrapProgram for `gh`/`pg-connector`: this backend execs both on PATH
  # at runtime — `gh` carried over unchanged from the ported GitHub Actions
  # client, and `pg-connector` composed for its own PRResolver (resolver.go)
  # — provisioned once, separately, wherever this workspace's home-manager
  # profile installs them, matching pg-connector-pr-github.nix's own choice
  # not to wrap `gh` [design: §9].

  meta = with lib; {
    description = "pg-connector's ci capability Tier-2 backend for GitHub Actions — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
