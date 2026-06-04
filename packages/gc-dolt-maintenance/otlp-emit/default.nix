{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:
mkBashLibrary {
  name = "gc-otlp-emit";
  src = ./.;
  description = "Best-effort OTLP/JSON metrics+logs emission for bash";
  inherit testSupport;
  testDeps = [
    pkgs.jq
    pkgs.curl
    pkgs.coreutils
  ];
}
