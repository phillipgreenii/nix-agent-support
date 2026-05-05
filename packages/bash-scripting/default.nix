{
  pkgs,
  lib,
  ...
}:

pkgs.stdenv.mkDerivation {
  pname = "bash-scripting";
  version = "0.1.0";
  src = ./.;

  dontBuild = true;

  installPhase = ''
    runHook preInstall

    mkdir -p $out/share/bash-scripting/skills
    cp -r skills/* $out/share/bash-scripting/skills/

    runHook postInstall
  '';

  meta = {
    description = "Claude skill: bash scripting conventions for the mkBashBuilders framework";
    platforms = lib.platforms.all;
  };
}
