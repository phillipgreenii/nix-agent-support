{
  config,
  lib,
  pkgs,
  inputs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  vscodeExts = inputs.nix-vscode-extensions.extensions.${pkgs.system};
in
{
  options.phillipgreenii.programs.claude.enable =
    lib.mkEnableOption "Claude Code and associated tooling";

  config = lib.mkIf cfg.enable {
    # Bridge to home-manager so HM modules can gate on the same flag
    home-manager.sharedModules = [
      { phillipgreenii.programs.claude.enable = lib.mkDefault cfg.enable; }
    ];
    home-manager.users.phillipg = {
      home.packages = [ pkgs.llm-agentsPkgs.claude-code ];

      programs.vscode.extensions = lib.mkAfter [
        vscodeExts.open-vsx."anthropic"."claude-code"
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
