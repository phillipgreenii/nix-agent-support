{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pb;
in
{
  options.phillipgreenii.programs.pb = {
    enable =
      lib.mkEnableOption ''
        pb (phillip-beads: writes and resolves pn:applied gates that hold beads
        until a `pn workspace apply` applies their change). `bd` and `git` are
        wired onto PATH via wrapProgram; `pn` MUST be provided on PATH by the
        environment (the apply post-hook env and dev shells already have it).
      ''
      // {
        # Bundle switch (design note docs/superpowers/specs/2026-11-07-mod-bundle-grouping-design.md,
        # "beads"): pb is the beads bundle's plugin, so it follows
        # phillipgreenii.programs.beads.enable by default. The default sits on this option,
        # not in the plugin's `requires`, because the manifest already requires `pb` for the
        # binary and an explicit pb.enable must still win. Through the fail-closed `requires`
        # gate the pb plugin turns off with the beads switch. `or false` reads a machine
        # that does not declare the beads option as off.
        default = config.phillipgreenii.programs.beads.enable or false;
      };
    package = lib.mkPackageOption pkgs "pb" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
