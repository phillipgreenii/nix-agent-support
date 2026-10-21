{ config, lib, ... }:

let
  cfg = config.phillipgreenii.programs.claude-auto-mode;

  # The base trust-slot prose for Claude Code's auto-mode classifier. These
  # entries are classifier input: no secrets, no account numbers, no IPs other
  # than loopback. Behavioral prohibitions already in CLAUDE.md are NOT
  # duplicated here. The ZR-specific layer lives in nix-ziprecruiter and is
  # appended after these via `lib.mkAfter`.
  baseEnvironment = [
    "Primary use of Claude Code: software development in the ZipRecruiter monorepo and in the user's personal nix repositories (nix-agent-support, nix-ziprecruiter, nix-repo-base), plus agent-tooling and beads issue-tracking work."
    "Host containment: Claude Code runs on an ordinary developer macOS machine with open internet; there is no container, VM or egress allow-list."
    "Cloud provider(s): AWS."
    "Secrets management: AWS access uses Granted profiles with credential_process in ~/.aws/config (no ~/.aws/credentials file; the SSO token lives in the OS keychain); credential values are never exported, printed or written to files."
    "Key internal services: a shared per-user Dolt server for the beads issue tracker listens on 127.0.0.1:25252 (launchd agent org.nixos.beads-dolt-server); local reads and writes through the bd CLI are routine."
    "Org-specific CLIs: bd, pn, pg-desk, pg-router, ccpool, pg-disk-reclaimer, session-mode, handoff-create, assume, tuicr, cmux, nb, pa-monitor and glowm are tools the user installed deliberately through nix/home-manager."
    "Source control: github.com/ZR-Private/* (the employer organization, ZipRecruiter), github.com/phillipgreenii/* and github.com/phillipgziprecruiter/* (the user's personal accounts) are trusted. The two personal accounts are trusted destinations only for content that originates in the user's nix-* repositories (nix-agent-support, nix-ziprecruiter, nix-repo-base), never for ZR-Private content."
  ];
in
{
  options.phillipgreenii.programs.claude-auto-mode = {
    enable = lib.mkEnableOption ''
      the base auto-mode trust slots: turns on nix ownership of the
      `autoMode` keys in `~/.claude/settings.json`
      (`phillipgreenii.programs.claude-code.settings.autoMode.manage`) and
      contributes the base `environment` entries. Layer-specific entries
      (for example the ZipRecruiter layer) are added by other modules
    '';
  };

  config = lib.mkIf cfg.enable {
    phillipgreenii.programs.claude-code.settings.autoMode = {
      manage = true;
      # mkBefore keeps the base layer first regardless of module evaluation
      # order; later layers use mkAfter.
      environment = lib.mkBefore baseEnvironment;
    };
  };
}
