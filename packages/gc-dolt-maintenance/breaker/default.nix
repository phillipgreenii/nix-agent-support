{
  mkBashScript,
  pkgs,
  gc-otlp-emit,
}:
mkBashScript {
  name = "gc-bd-import-breaker";
  src = ./.;
  description = "Pin <city>/.beads/issues.jsonl as immutable-empty to stop bd's auto-import spiral (HACK 18)";
  libraries = [ gc-otlp-emit ];
  runtimeDeps = [
    pkgs.coreutils
    pkgs.curl
    pkgs.jq
  ];
  testDeps = [
    pkgs.coreutils
    pkgs.curl
    pkgs.jq
  ];
}
