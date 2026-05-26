{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-pack-test-fixture";
  src = ./pack-src;
  meta = with lib; {
    description = "Trivial pack for validating mkPgiiPack + pgii-packs activation pipeline.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
