{ ... }:
{
  imports = [
    ./modules/pg2-agent
    ./modules/claude-code
    ./modules/pa-monitor
    ./modules/pr-pool
    ./modules/ccpool
    ./modules/gc-dolt-maintenance
    ./services/beads-web
  ];
}
