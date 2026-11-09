{
  config,
  lib,
  options,
  ...
}:
{
  # NixOS counterpart of darwin/modules/beads: the machine-wide policy flag
  # forbidding *transparent* dolt auto-start. Same option name and default as
  # the darwin module so one policy spelling works on both machine classes.
  # See docs/adr/0032-beads-dolt-no-autostart.md.
  #
  # Unlike darwin, enforcement here does not depend on the consumer's `bd`
  # wrapper: the flag exports `BEADS_DOLT_AUTO_START=0` for every login
  # session through `environment.variables`, and propagates to home-manager
  # (agent-rules module) as a specialArg. `localDoltLaunchdServer` has no
  # NixOS equivalent (there is no launchd), so the agent-rules default (false)
  # keeps launchd text out of NixOS machines.
  options.phillipgreenii.beads.forbidDoltAutoStart = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      Forbid transparent dolt auto-start on this machine. When enabled (default;
      secure-by-default) `BEADS_DOLT_AUTO_START=0` is exported system-wide and
      the beads/dolt no-autostart agent rule plus the `beads-dolt-doctor`
      skill are delivered through home-manager. Set `false` on a machine where
      dolt auto-start is acceptable.
    '';
  };

  config = lib.mkMerge [
    {
      environment.variables = lib.mkIf config.phillipgreenii.beads.forbidDoltAutoStart {
        BEADS_DOLT_AUTO_START = "0";
      };
    }
    # home-manager's NixOS module may not be imported (standalone evaluation).
    (lib.optionalAttrs (options ? home-manager) {
      home-manager.extraSpecialArgs = {
        forbidDoltAutoStart = config.phillipgreenii.beads.forbidDoltAutoStart;
      };
    })
  ];
}
