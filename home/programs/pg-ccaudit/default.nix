{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-ccaudit;
in
{
  options.phillipgreenii.programs.pg-ccaudit = {
    enable = lib.mkEnableOption ''
      pg-ccaudit (indexes Claude Code transcripts into SQLite so a tool-error
      waste review is a QUERY rather than a multi-gigabyte JSONL scan). Scanning
      the corpus raw is what stalled two supervised agent runs; the shipped review
      skill's first instruction is therefore to query the database and never to
      read the transcripts directly
    '';
    package = lib.mkPackageOption pkgs "pg-ccaudit" { };

    databasePath = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.dataHome}/pg-ccaudit/transcripts.db";
      defaultText = lib.literalExpression ''"''${config.xdg.dataHome}/pg-ccaudit/transcripts.db"'';
      description = ''
        Where the index lives. GLOBAL, not per-project — cross-project analysis
        over every project directory is the point of the index, so a per-repo
        database would answer none of the questions it exists for. Follows the
        `~/.local/share/beads-dolt` precedent.
      '';
    };

    transcriptRoot = lib.mkOption {
      type = lib.types.str;
      default = "${config.home.homeDirectory}/.claude/projects";
      defaultText = lib.literalExpression ''"''${config.home.homeDirectory}/.claude/projects"'';
      description = "Root of the Claude Code transcript tree to index.";
    };

    sweep = {
      enable = lib.mkEnableOption ''
        the scheduled pg-ccaudit ingest sweep (a launchd user agent on darwin).

        This is deliberately a TIMER and deliberately NOT a Claude Code
        session-end hook. A session-end hook only fires when a session terminates
        cleanly, and abnormally-killed sessions are precisely the interesting ones
        — a stalled or crashed session is itself evidence of the waste being
        measured — so hooking session end would systematically omit the strongest
        signal in the corpus. The unit itself is registered by the parallel darwin
        module (per `phillipgreenii-nix-personal` ADR 0049); this is the
        public-facing enable flag
      '';

      intervalSeconds = lib.mkOption {
        type = lib.types.int;
        default = 900;
        description = ''
          How often (seconds) to run `pg-ccaudit ingest`. 900 (~15 minutes) is
          affordable only because ingest resumes each transcript from a stored
          byte offset and parses just the appended delta; without that the active
          session's file would be re-parsed in full on every tick.
        '';
      };

      thinking = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Also index thinking blocks. DEFAULT OFF: they are ~94 MB corpus-wide
          and no shipped query reads them. Turning this on creates an additional
          `thinking` table; the default database carries exactly the specified
          schema and nothing else.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # The CLI resolves both paths from these variables, so the launchd agent, an
    # interactive shell and the review skill all address the SAME index without
    # any of them hardcoding a path.
    home.sessionVariables = {
      PG_CCAUDIT_DB = cfg.databasePath;
      PG_CCAUDIT_ROOT = cfg.transcriptRoot;
    };
  };
}
