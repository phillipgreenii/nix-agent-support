{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-dolt-hacks";
  src = ./pack-src;
  meta = with lib; {
    description = "HACK orders for dolt storage/lifecycle issues and gascity supervisor regressions (HACK 2, 10, 11, 12, 14, 15, 16).";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
