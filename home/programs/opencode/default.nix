{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.opencode;
  ollamaCfg = config.phillipgreenii.programs.ollama;

  # Build an OpenCode `provider.ollama` block from the local
  # `phillipgreenii.programs.ollama` daemon's options. Only emitted
  # when the ollama module is also enabled.
  ollamaModelsAttrs = builtins.listToAttrs (
    map (m: {
      name = m;
      value = {
        name = m;
      };
    }) ollamaCfg.loadModels
  );
  ollamaProviderEntry = lib.optionalAttrs ollamaCfg.enable {
    ollama = {
      # ollama-ai-provider-v2 is the Ollama-native AI SDK adapter. The
      # generic @ai-sdk/openai-compatible shim leaks raw tool-call JSON
      # back to the user instead of letting opencode parse and execute
      # it; the native adapter speaks ollama's /api/chat protocol and
      # handles tool calls correctly.
      npm = "ollama-ai-provider-v2";
      name = "Ollama (local)";
      options.baseURL = "http://${ollamaCfg.host}:${toString ollamaCfg.port}/api";
      models = ollamaModelsAttrs;
    };
  };
in
{
  options.phillipgreenii.programs.opencode = {
    enable = lib.mkEnableOption "OpenCode local-first AI coding agent";

    model = lib.mkOption {
      type = lib.types.str;
      default = "ollama/qwen3:8b";
      description = ''
        Provider/model identifier OpenCode routes prompts to.
        Default points at the Ollama daemon enabled by
        `phillipgreenii.programs.ollama`. Match the model name to one
        of the entries in that module's `loadModels`.
      '';
    };

    autoshare = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Whether OpenCode auto-shares sessions to its hosted dashboard.";
    };

    providers = lib.mkOption {
      type = lib.types.attrs;
      default = ollamaProviderEntry;
      defaultText = lib.literalExpression ''
        { ollama = ...; }  # auto-wired from phillipgreenii.programs.ollama
                           # when that module is enabled, else { }
      '';
      description = ''
        OpenCode provider definitions rendered into the `provider` key of
        `~/.config/opencode/config.json`. The default auto-wires the local
        Ollama daemon configured by `phillipgreenii.programs.ollama` (one
        provider entry per declared `loadModels`). Override to register
        additional providers or to point opencode at a remote Ollama.
      '';
    };

    extraConfig = lib.mkOption {
      type = lib.types.attrs;
      default = { };
      description = ''
        Extra keys merged into ~/.config/opencode/config.json. Top-level
        keys here override the typed options (`model`, `autoshare`,
        `providers`) and the `$schema` line — useful as an escape hatch
        for fields that don't yet have first-class options.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = pkgs ? llm-agentsPkgs;
        message = ''
          phillipgreenii.programs.opencode.enable requires the
          mkLlmAgentsOverlay (provides `pkgs.llm-agentsPkgs`). Apply it at
          the consuming machine config (see
          your-flake/machines/default.nix).
        '';
      }
    ];

    home.packages = [ pkgs.llm-agentsPkgs.opencode ];

    # Mirror the `pkgs.writeText` / `.source` idiom used by
    # `home/programs/cmux/default.nix`. Yields readable diffs.
    xdg.configFile."opencode/config.json".source =
      (pkgs.formats.json { }).generate "opencode-config.json"
        (
          {
            "$schema" = "https://opencode.ai/config.json";
            inherit (cfg) model autoshare;
            provider = cfg.providers;
          }
          // cfg.extraConfig
        );
  };
}
