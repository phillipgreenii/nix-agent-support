{ ... }:
{
  imports = [
    ./modules/pg2-agent
    ./modules/claude-code
    ./services/beads
    ./services/beads-web
    ./services/beads-dolt-server
  ];
}
