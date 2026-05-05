{
  pkgs,
  lib,
  gitHash,
  ...
}:

let
  pythonPackageLib = import ../../lib/python-package.nix { inherit pkgs lib; };
in

pythonPackageLib.mkPythonPackage {
  name = "gh-prreview";
  inherit gitHash;
  src = ./.;

  pypiToNixNameMappings = {
    "click" = "click";
    "rich" = "rich";
    "httpx" = "httpx";
  };

  runtimeDeps = [
    pkgs.gh
    pkgs.git
  ];

  versionPlaceholder = "0.0.0";
  versionInitFile = "src/gh_prreview/__init__.py";

  hasCompletions = true;
  hasTldr = true;
}
