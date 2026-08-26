{
  config,
  lib,
  ...
}:
{
  # Machine-wide policy flag: forbid *transparent* dolt auto-start.
  #
  # When enabled, the machine-wide `bd` derivation exports
  # `BEADS_DOLT_AUTO_START=0` (enforcement wired in the consumer's overlay /
  # pkgs/beads/package.nix wrapper), so no context on this machine — interactive
  # shells, GUI apps, or launchd daemons (user or root) — silently spawns a
  # second, config-less `dolt sql-server` that steals `127.0.0.1:25252` from the
  # shared `org.nixos.beads-dolt-server` agent. See
  # docs/adr/0032-beads-dolt-no-autostart.md and the design at
  # docs/superpowers/specs/2026-07-24-beads-dolt-no-autostart-design.md.
  #
  # Declared at DARWIN scope (not home-manager) because the enforcement
  # chokepoint — the `bd` overlay/`package.nix` wrapper — is darwin/pkgs-scoped;
  # a home-manager option cannot gate it (design §6.1). Plain `mkOption` (not a
  # bundle `mkDefault`) so there is no opposing-`mkDefault` conflict risk.
  options.phillipgreenii.beads.forbidDoltAutoStart = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      Forbid transparent dolt auto-start on this machine. When enabled (default;
      secure-by-default per design P5) the machine-wide `bd` derivation exports
      `BEADS_DOLT_AUTO_START=0`, and the beads/dolt no-autostart agent rule plus
      the `beads-dolt-doctor` debugging skill are delivered — so no context,
      including launchd daemons, transparently auto-starts a `dolt sql-server`
      competing for `127.0.0.1:25252`. Set `false` on a machine where dolt
      auto-start is acceptable.
    '';
  };

  # Whether THIS machine runs the shared per-user launchd dolt server
  # (`org.nixos.beads-dolt-server` on `127.0.0.1:25252`; the launchd service
  # itself is defined in the consuming machine flake, not here). Gates the
  # Mac-local launchd portion of the agent rule (the rogue-server smell, the
  # `beads-dolt-doctor` pointer) separately from the generic no-autostart
  # posture: that text is only TRUE where the launchd server exists, and on a
  # machine using a remote dolt server it contradicts the real policy. Default
  # true at DARWIN scope, same plain-`mkOption` style as above: importing this
  # darwin module means the machine class that runs the launchd server. NixOS
  # machines never import it, so the agent-rules module's own default (false)
  # applies there and no launchd text renders.
  options.phillipgreenii.beads.localDoltLaunchdServer = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = ''
      This machine runs the shared per-user launchd dolt server
      (`org.nixos.beads-dolt-server`, port 25252). When enabled (default on
      darwin), the Mac-local launchd section of the beads/dolt agent rule is
      delivered alongside the generic no-autostart posture. Set `false` on a
      darwin machine that does not run the local launchd server.
    '';
  };

  # Propagate the flags INTO home-manager so HM-scoped consumers (the
  # agent-rules module, the `beads-dolt-doctor` skill enablement, the vscode
  # extension removal) read the SAME values as specialArgs (design P6: one flag,
  # nothing hardcodes the policy). Set unconditionally — the value itself
  # carries the policy — mirroring how the other darwin modules write
  # `home-manager.*` in this repo.
  config.home-manager.extraSpecialArgs = {
    forbidDoltAutoStart = config.phillipgreenii.beads.forbidDoltAutoStart;
    localDoltLaunchdServer = config.phillipgreenii.beads.localDoltLaunchdServer;
  };
}
