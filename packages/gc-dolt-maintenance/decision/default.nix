{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:
mkBashLibrary {
  name = "gc-dolt-maintenance-lib";
  src = ./.;
  description = "Pure should_flatten decision function for dolt maintenance";
  inherit testSupport;
  testDeps = [ pkgs.coreutils ];
}
