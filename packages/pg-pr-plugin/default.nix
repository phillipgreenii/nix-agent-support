{
  pkgs,
  lib,
  ...
}:

pkgs.stdenv.mkDerivation {
  pname = "pg-pr-plugin";
  version = "0.1.0";
  src = ./share/pg-pr-plugin;

  dontBuild = true;

  installPhase = ''
    runHook preInstall

    mkdir -p $out/share/pg-pr-plugin
    cp -r . $out/share/pg-pr-plugin/

    runHook postInstall
  '';

  meta = with lib; {
    description = "Claude plugin content for pg-pr: skills, commands, agents, hooks";
    platforms = platforms.all;
  };
}
