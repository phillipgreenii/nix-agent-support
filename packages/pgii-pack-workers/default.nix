{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-workers";
  src = ./pack-src;
  meta = with lib; {
    description = "Rig-scoped generic worker agent (claims beads with acceptance_criteria).";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
