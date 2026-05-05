{
  mkBashScript,
  pkgs,
  agents,
}:

mkBashScript {
  name = "pg2-agent";
  src = ./.;
  description = "AI agent wrapper with plugin architecture";
  runtimeDeps = [ pkgs.jq ];
  testDeps = [ pkgs.jq ];
  config = {
    AGENTS_CONFIG = agents;
  };
}
