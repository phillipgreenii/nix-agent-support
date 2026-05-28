{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-dolt-hacks";
  src = ./pack-src;
  meta = with lib; {
    description = "HACK orders for dolt storage/lifecycle issues and gascity 1.1.0 supervisor regressions (HACK 2, 10, 11, 12, 14, 15 + hack-daily-summary).";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
