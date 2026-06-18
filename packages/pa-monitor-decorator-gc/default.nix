{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pa-monitor-decorator-gc";

  src = lib.cleanSource ./.;

  gomod2nixToml = ./gomod2nix.toml;

  meta = {
    description = "Gas City–specific label decorator for pa-monitor. Reads pa-monitor session JSON on stdin (PA_MONITOR_DECORATE=1) and emits org-specific labels on stdout. See ADR-0011.";
    mainProgram = "pa-monitor-decorator-gc";
  };
}
