{
  pkgs,
  lib,
  ...
}:

pkgs.stdenv.mkDerivation {
  pname = "my-code-review-support";
  version = "0.2.0";
  src = ./.;

  dontBuild = true;

  installPhase = ''
    runHook preInstall

    # Install skills
    mkdir -p $out/share/my-code-review-support/skills
    cp -r skills/* $out/share/my-code-review-support/skills/

    # Install agents
    mkdir -p $out/share/my-code-review-support/agents
    cp agents/*.md $out/share/my-code-review-support/agents/

    # Install references
    mkdir -p $out/share/my-code-review-support/references
    cp references/*.md $out/share/my-code-review-support/references/

    runHook postInstall
  '';

  meta = with lib; {
    description = "Code review support plugin - skills, agents, and reference guidelines";
    platforms = platforms.all;
  };
}
