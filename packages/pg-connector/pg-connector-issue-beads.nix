{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-issue-beads";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # ./pg-connector-pr-github.nix's sibling backend — one go.mod, one
  # gomod2nix.toml, a further mkGoApp call building a third binary out of
  # it; this packet does not create a second Go module [design: §5.2, §5.3
  # AC].
  src = ./.;
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-issue-beads" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and pg-connector-pr-github.nix's own convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (matches pg-connector-pr-github.nix):
  # this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # [design: §5 preamble].
  #
  # No wrapProgram for `bd`: this backend execs `bd` on PATH at runtime,
  # matching pg-connector-pr-github.nix's identical decision for `gh` — `bd`
  # is provisioned once, separately, wherever this workspace's home-manager
  # profile installs it, not per-consumer here.

  meta = with lib; {
    description = "pg-connector's issue capability Tier-2 backend for this workspace's own bd tracker — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
