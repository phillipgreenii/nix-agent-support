{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-foremen";
  src = ./pack-src;
  meta = with lib; {
    description = "Category triager + city/zr/personal foremen + HQ city-worker + mol-triage-poll order.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
