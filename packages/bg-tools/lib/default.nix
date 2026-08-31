{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "bg-tools-lib";
  src = ./.;
  description = "Shared state conventions for the bgrun/bgcheck background-job helpers";
  inherit testSupport;
  testDeps = [
    pkgs.coreutils
  ];
}
