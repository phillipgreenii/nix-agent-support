{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-router-sensors";

  # Pattern B (phillipg-nix-repo-base ADR 0008): the module imports the
  # level-answer schema validator from `../pg-router` (and that module's own
  # local replace `../claude-transcript`), so root the fileset at the parent
  # and pass modRoot, as pg-router-handlers does.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-router
      ../claude-transcript
    ];
  };
  modRoot = "pg-router-sensors";

  gomod2nixToml = ./gomod2nix.toml;

  # main.go declares `var version` in lowercase; mkGoApp's default ldflag
  # target is main.Version.
  versionPath = "main.version";

  subPackages = [ "cmd/pg-router-sensors" ];

  meta = {
    description = "The router's sensors: one binary whose subcommands each print JSONL level answers (host, vpn, git-auth, beads, claude, aws-sso)";
    mainProgram = "pg-router-sensors";
  };
}
