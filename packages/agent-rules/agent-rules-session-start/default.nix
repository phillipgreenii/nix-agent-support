{
  mkBashScript,
  pkgs,
  rulesFile,
  testSupport ? null,
}:

# SessionStart hook that injects the always-on agent rules as additionalContext.
# The rules content is the single source of truth: `rulesFile`
# (home/programs/agent-rules/pgii-agent-rules.md) is baked in as the
# AGENT_RULES_FILE store path via `config`, so the script and its bundled
# pgii-agent-rules.md never diverge.
mkBashScript {
  name = "agent-rules-session-start";
  src = ./.;
  description = "Claude Code SessionStart hook injecting always-on agent rules as additionalContext";
  runtimeDeps = [ pkgs.jq ];
  testDeps = [ pkgs.jq ];
  config = {
    AGENT_RULES_FILE = "${rulesFile}";
  };
  inherit testSupport;
}
