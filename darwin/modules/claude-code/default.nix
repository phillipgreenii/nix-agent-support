{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude-code;
in
{
  options.phillipgreenii.programs.claude-code.enable =
    lib.mkEnableOption "Claude Code and associated tooling";

  config = lib.mkIf cfg.enable {
    # Bridge to home-manager so HM modules can gate on the same flag
    home-manager.sharedModules = [
      { phillipgreenii.programs.claude-code.enable = lib.mkDefault cfg.enable; }
    ];
    home-manager.users.phillipg = {
      # `claude doctor` / `/doctor` reports a BENIGN warning here, safe to ignore (pg2-zv2v):
      #   "Native installation exists but ~/.local/bin is not in your PATH.
      #    Run: echo 'export PATH=\"$HOME/.local/bin:$PATH\"' >> ~/.zshrc"
      # Root cause: Claude Code's install-type probe (U_e) flags any Bun-compiled
      # standalone binary (Rf() == Bun.embeddedFiles non-empty) that isn't under
      # node_modules or a *recognized* package manager (brew/apt/...) as "native".
      # The nix build IS such a standalone binary and nix is not recognized, so it
      # self-labels "native"; the doctor then warns that ~/.local/bin isn't on PATH.
      # That warning fires OUTSIDE the DISABLE_INSTALLATION_CHECKS guard, so the
      # upstream (numtide/llm-agents.nix) wrapper's DISABLE_INSTALLATION_CHECKS=1
      # cannot suppress it.
      # It is a false positive: nothing is installed in ~/.local/bin (and the binary
      # doesn't live there); the nix-pinned claude IS the binary executed; and the
      # wrapper sets DISABLE_AUTOUPDATER=1 so it cannot self-update / shadow the pin.
      # DO NOT apply doctor's fix (raw `>> ~/.zshrc`): PATH/installs are nix-managed.
      home.packages = [ pkgs.llm-agentsPkgs.claude-code ];

      programs.vscode.profiles.default.extensions = lib.mkAfter [
        pkgs.vscode-extensions.anthropic.claude-code
      ];
    };

    phillipgreenii.programs.pg2-agent = {
      enable = true;
      agents.claude = {
        id = "claude";
        priority = 10;
        script = pkgs.writeShellScript "claude-agent" (import ./agent-script.nix { inherit pkgs; });
      };
    };
  };
}
