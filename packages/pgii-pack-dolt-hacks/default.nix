{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-dolt-hacks";
  src = ./pack-src;
  meta = with lib; {
    description = "HACK orders for dolt storage/lifecycle issues (HACK 2, 10, 14, 15, 16). HACK 11 (mol-dog-jsonl wrapper) + HACK 12 (order-override-watchdog) retired on gascity 1.2.1, which honors [[orders.overrides]] and ships the issue_type jsonl-export fix.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
