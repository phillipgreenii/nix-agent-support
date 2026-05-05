{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.ollama;
  # Resolve the package lazily — `pkgs.unstable` is only present when the
  # consuming machine applies `mkUnstableOverlay` (the case for
  # phillipg-mbp-02). Falling back to `pkgs.ollama` keeps standalone
  # evaluation of this module functional.
  defaultOllama = if pkgs ? unstable then pkgs.unstable.ollama else pkgs.ollama;
  wrapper = import ./wrapper.nix {
    inherit pkgs lib;
    ollamaPackage = cfg.package;
  };
in
{
  options.phillipgreenii.programs.ollama = {
    enable = lib.mkEnableOption "Ollama local inference runtime via launchd user agent";

    host = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1";
      description = "Address the Ollama HTTP server binds to.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 11434;
      description = "Port the Ollama HTTP server listens on.";
    };

    package = lib.mkOption {
      type = lib.types.package;
      default = defaultOllama;
      defaultText = lib.literalExpression "pkgs.unstable.ollama or pkgs.ollama";
      description = ''
        Ollama package. Defaults to `pkgs.unstable.ollama` when the consuming
        machine applies the unstable overlay, otherwise `pkgs.ollama`. Apple
        Silicon Metal acceleration and ollama releases ship on
        nixpkgs-unstable noticeably ahead of 25.11.
      '';
    };

    loadModels = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "qwen3:8b" ];
      example = [
        "qwen3:8b"
        "qwen3-coder:30b"
      ];
      description = ''
        Models to pull on first launchd start. Already-present models are
        not re-pulled.

        Default is `qwen3:8b` — small (~5 GB), fast, and reliably emits
        structured tool calls (qwen2.5-coder advertised tools but its
        chat template degraded to plain-text JSON, breaking OpenCode).
        Swap to `qwen3-coder:30b` (~18 GB MoE, 3B active) for stronger
        coding output if RAM headroom allows.
      '';
    };

    extraEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {
        OLLAMA_NUM_GPU = "999";
        OLLAMA_KEEP_ALIVE = "24h";
        OLLAMA_FLASH_ATTENTION = "1";
        OLLAMA_CONTEXT_LENGTH = "16384";
      };
      example = {
        OLLAMA_DEBUG = "1";
      };
      description = ''
        Extra environment variables for the launchd ollama agent. Defaults
        are tuned for Apple Silicon Metal:

        - `OLLAMA_NUM_GPU=999` forces max layer offload to Metal, overriding
          ollama's conservative auto-detection that may fall back to CPU
          under unified-RAM pressure.
        - `OLLAMA_KEEP_ALIVE=24h` keeps the model resident in RAM so prompts
          after a 5-minute idle don't pay the multi-gigabyte reload cost.
        - `OLLAMA_FLASH_ATTENTION=1` enables Flash Attention v2; ~10-20%
          speedup and lower KV-cache memory at long context.
        - `OLLAMA_CONTEXT_LENGTH=16384` raises the default context window
          from ollama's 2048-token default. Tool-using agents (e.g.
          OpenCode) need at least 16k for system prompt + tool schemas +
          conversation; otherwise they leak raw JSON instead of emitting
          structured tool calls. ~3 GB additional KV-cache RAM at 16k
          for a 14B Q4 model; fits comfortably on 36 GB unified.

        Set to `{ }` for vanilla ollama defaults. `OLLAMA_HOST` is wired
        from `host`/`port` and applied separately.
      '';
    };

    processType = lib.mkOption {
      type = lib.types.enum [
        "Background"
        "Standard"
        "Adaptive"
        "Interactive"
      ];
      default = "Adaptive";
      description = ''
        launchd ProcessType for the ollama agent. `Adaptive` (default) is
        appropriate for an interactive coding daemon: macOS boosts CPU/GPU
        priority while user-facing windows are foreground and relaxes when
        idle. `Background` will be throttled by the scheduler and is the
        wrong choice for a latency-sensitive workload.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # Ensure interactive `ollama` CLI talks to this user's daemon by default.
    home.sessionVariables.OLLAMA_HOST = "${cfg.host}:${toString cfg.port}";

    launchd.agents.ollama = {
      enable = true;
      config = {
        Label = "phillipgreenii.ollama";
        ProgramArguments = [ "${wrapper}/bin/pgii-ollama-server" ] ++ cfg.loadModels;
        EnvironmentVariables = {
          OLLAMA_HOST = "${cfg.host}:${toString cfg.port}";
        }
        // cfg.extraEnv;
        KeepAlive = true;
        RunAtLoad = true;
        ProcessType = cfg.processType;
        StandardOutPath = "${config.home.homeDirectory}/Library/Logs/ollama.out.log";
        StandardErrorPath = "${config.home.homeDirectory}/Library/Logs/ollama.err.log";
      };
    };
  };
}
