{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-pr-support";
  src = ./pack-src;
  meta = with lib; {
    description = "PR review / triage / self-fix agents + PR-related doctor checks.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
