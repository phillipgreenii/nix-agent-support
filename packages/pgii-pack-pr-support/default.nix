{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-pr-support";
  src = ./pack-src;
  meta = with lib; {
    description = "PR review / triage / self-fix agents + pr-watcher / wake-on-work orders.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
