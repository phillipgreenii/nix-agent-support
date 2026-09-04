{
  lib,
  mkGoApp,
  ...
}:

mkGoApp {
  pname = "pg-connector-pr-github";

  # Shares the SAME Go module as ./default.nix's cmd/pg-connector build —
  # one go.mod, one gomod2nix.toml, two mkGoApp calls building two different
  # binaries out of it; this packet does not create a second Go module
  # [design: §5.2, §5.3].
  src = ./.;
  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-connector-pr-github" ];

  # This package exports its version as `main.Version` (capitalised),
  # mirroring default.nix's own cmd/pg-connector build.
  versionPath = "main.Version";

  # No help2man/completions postInstall (unlike default.nix's cmd/pg-connector
  # build): this binary speaks only the scriptout wire protocol and has no
  # independent CLI identity a human types directly, so there is no --help
  # output to generate a man page from and no subcommands to complete
  # [design: §5 preamble; freedom boundary, part 4].
  #
  # No wrapProgram for `gh`: this backend execs `gh` on PATH at runtime,
  # carried over unchanged from pg-pr's own github provider, which likewise
  # ships with no gh wrapping in packages/pg-pr/default.nix — `gh` is
  # provisioned once, separately, wherever this workspace's home-manager
  # profile installs it, not per-consumer here [design: §9].

  meta = with lib; {
    description = "pg-connector's pr capability Tier-2 backend for GitHub — a scriptout-only binary with no independent CLI identity";
    platforms = platforms.all;
  };
}
