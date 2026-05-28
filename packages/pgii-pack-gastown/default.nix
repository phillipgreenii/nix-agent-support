{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-gastown";
  src = ./pack-src;
  meta = with lib; {
    description = "Local gastown-derived agents (mayor/deacon/operator/foreman) + mol-deacon-patrol formula + 3 doctor checks.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
