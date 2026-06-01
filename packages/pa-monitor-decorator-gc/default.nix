{
  lib,
  buildGoModule,
  version ? "dev",
}:

buildGoModule {
  pname = "pa-monitor-decorator-gc";
  inherit version;

  src = lib.cleanSource ./.;

  # No external dependencies yet — the scaffold uses only stdlib.
  # `vendorHash = null` skips vendor-fetching entirely. Switch to a
  # real hash once `decorate()` grows imports (re-run update-deps.sh).
  vendorHash = null;

  ldflags = [
    "-X main.version=${version}"
  ];

  meta = {
    description = "Gas City–specific label decorator for pa-monitor. Reads pa-monitor session JSON on stdin (PA_MONITOR_DECORATE=1) and emits org-specific labels on stdout. See ADR-0011.";
    mainProgram = "pa-monitor-decorator-gc";
  };
}
