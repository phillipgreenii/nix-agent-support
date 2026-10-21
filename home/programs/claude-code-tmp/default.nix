{
  config,
  lib,
  options,
  ...
}:

# claude_code_tmp (bead pg2-2qirl, packet A): the sibling root for Claude Code's
# own temp tree (CLAUDE_CODE_TMPDIR; scratchpads, background-task output). Same
# shape as build-tmp (lib/tmp-root.nix) with more relaxed idle times, because a
# session's scratchpad can be days old and still wanted; its sweep works per
# session directory (packet B). Claude appends `claude-<uid>/` under the root.
# Facts and tested exclusions: docs/guides/build-tmp-roots.md.

(import ../../../lib/tmp-root.nix { inherit lib; }).mkModule {
  name = "claude-code-tmp";
  basename = "claude_code_tmp";
  marker = ".claude-code-tmp-root";
  activationName = "claudeCodeTmpRoot";
  summary = "Claude Code's own temp tree (CLAUDE_CODE_TMPDIR), swept per idle session";

  envVars = _cfg: [ "CLAUDE_CODE_TMPDIR" ];

  level1Default = 20160;
  level3Default = 2880;
  level1Doc = "Default 20160 (14 days): finished sessions' scratch, nothing to regenerate.";
  level3Doc = "Default 2880 (2 days): scratch of sessions idle for days is rarely wanted but is not regenerable, so it is kept longer than build output.";

  # Registry item (packet B), consumed by lib/tmp-root.nix. The reasons below are
  # the justifying comments docs/aggressiveness-scale.md requires for a level
  # whose choice is not obvious.
  sweepDepth = 3;
  sweepLiveness = "claude-session";
  itemDescription = "Claude Code session scratch (per session directory) idle past the cutoff";
  # Level 1 (tidy): finished sessions' scratch, untouched for the whole idle
  # period; nothing to regenerate.
  level1Reason = "finished sessions' scratch, nothing to regenerate";
  # Level 3 (deep): scratch of a session idle for days is rarely wanted but is not
  # regenerable, so the idle time is longer than for build output. It stays below
  # the confirmation gate because the live-session guard (session uuid in argv,
  # open files) protects anything in use. Known gap: a headless claude -p worker
  # has a session directory whose uuid is not in argv, so only the idle cutoff
  # protects it.
  level3Reason = "scratch of a session idle for days is rarely wanted but not regenerable";
} { inherit config options; }
