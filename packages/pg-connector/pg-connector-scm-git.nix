{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-scm-git";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build and
  # ./pg-connector-pr-github.nix's cmd/pg-connector-pr-github build — one
  # go.mod, one gomod2nix.toml, a third mkGoApp call building a third
  # binary out of it; this packet does not create a second Go module
  # [design: §5.2, §5.3].
  src = ./.;
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-scm-git" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's and pg-connector-pr-github.nix's own
  # cmd/pg-connector*/main.go convention.
  versionPath = "main.Version";

  # No help2man/completions postInstall (unlike default.nix's cmd/pg-connector
  # build): this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # [design: §5 preamble].
  #
  # No wrapProgram for `git`: this backend execs `git` on PATH at runtime —
  # `git` is provisioned once, separately, wherever this workspace's
  # home-manager profile installs it, not per-consumer here, mirroring
  # pg-connector-pr-github.nix's own no-wrapProgram-for-gh rationale.

  meta = with lib; {
    description = "pg-connector's scm capability Tier-2 backend for local git — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
