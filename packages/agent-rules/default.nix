{
  pkgs,
  bashBuilders,
  rulesFile,
}:
let
  agent-rules-session-start = pkgs.callPackage ./agent-rules-session-start {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs rulesFile;
  };
in
{
  inherit agent-rules-session-start;
  inherit (agent-rules-session-start) packages;
  checks = {
    test-agent-rules-session-start = agent-rules-session-start.check;
  };
}
