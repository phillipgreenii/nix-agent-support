{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pa-monitor-decorator-scope";

  src = lib.cleanSource ./.;

  gomod2nixToml = ./gomod2nix.toml;

  meta = {
    description = "Generic path->scope label decorator for pa-monitor. Reads pa-monitor session JSON on stdin (PA_MONITOR_DECORATE=1) and maps the session CWD to a workspace.scope label by longest-prefix match over -rule PREFIX=SCOPE rules (env fallback PA_MONITOR_SCOPE_RULES). See ADR-0024 D5.";
    mainProgram = "pa-monitor-decorator-scope";
  };
}
