{ ... }:
{
  imports = [
    ./modules/beads
    ./modules/beads-exporter
    ./modules/pg2-agent
    ./modules/claude-code
    ./modules/pa-monitor
    ./modules/pg-router
    ./modules/pg-connector-pr-github
    ./modules/pg-connector-thread-slack
    ./modules/pg-connector-issue-jira
    ./modules/pg-rescue
    ./modules/pg-desk-serve
    ./modules/ccpool
    ./modules/pg-router-ccpool-handler
    ./modules/pg-ccaudit
    ./modules/ollama
    ./modules/codeburn
  ];
}
