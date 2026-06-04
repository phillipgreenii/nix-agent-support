{
  pkgs,
  bashBuilders,
  gascity,
}:
let
  testSupport = ./test-support;

  gc-otlp-emit = pkgs.callPackage ./otlp-emit {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  gc-dolt-maintenance-lib = pkgs.callPackage ./decision {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  gc-bd-import-breaker = pkgs.callPackage ./breaker {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs gc-otlp-emit;
  };

  gc-dolt-maintenance = pkgs.callPackage ./maintenance {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      gc-otlp-emit
      gc-dolt-maintenance-lib
      gascity
      ;
  };

  allScripts = [
    gc-bd-import-breaker
    gc-dolt-maintenance
  ];
in
{
  inherit
    gc-otlp-emit
    gc-dolt-maintenance-lib
    gc-bd-import-breaker
    gc-dolt-maintenance
    ;
  packages = builtins.concatLists (map (s: s.packages) allScripts);
  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;
  checks = {
    test-gc-otlp-emit = gc-otlp-emit.check;
    test-gc-dolt-maintenance-lib = gc-dolt-maintenance-lib.check;
    test-gc-bd-import-breaker = gc-bd-import-breaker.check;
    test-gc-dolt-maintenance = gc-dolt-maintenance.check;
  };
}
