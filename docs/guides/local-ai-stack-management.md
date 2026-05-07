# Local AI Stack: Day-to-Day Management

Quick reference for running the local AI stack (Ollama daemon + OpenCode
agent). For first-time setup, see
[local-ai-stack-getting-started.md](./local-ai-stack-getting-started.md).

## Daemon Lifecycle

The Ollama daemon runs as a launchd user agent labelled
`phillipgreenii.ollama`. The wrapper invokes `ollama serve` and pulls any
declared models on first start.

### Status

```bash
launchctl list | grep ollama
# Numeric PID = running. Dash = loaded but not currently a process.

curl -fsS http://127.0.0.1:11434/ && echo
# "Ollama is running" = HTTP-responsive
```

### Stop temporarily

```bash
# Just unload the model (frees ~5 GB; daemon stays). Cold-load on next prompt.
ollama stop qwen3:8b

# Force unload all models via API (keep_alive=0 on next pull)
curl -fsS http://127.0.0.1:11434/api/generate -d '{"model":"qwen3:8b","keep_alive":0}'
```

### Stop the daemon entirely

```bash
launchctl bootout gui/$UID/phillipgreenii.ollama
```

`KeepAlive = true` in the plist means `launchctl kill SIGTERM ...` would
respawn the daemon. Use `bootout` to actually stop.

### Start / bring it back

```bash
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/phillipgreenii.ollama.plist
# Or simply log out and back in — RunAtLoad triggers it.
```

### Restart (reload after config change)

```bash
launchctl kickstart -k gui/$UID/phillipgreenii.ollama
```

The `-k` kills any running instance first. Use this after `darwin-rebuild
switch` if you want the daemon to pick up new env vars or model lists
immediately rather than waiting for the next idle restart.

## Model Management

### List installed

```bash
ollama list
# NAME / ID / SIZE / MODIFIED
```

### List currently loaded into RAM

```bash
ollama ps
# PROCESSOR column shows GPU/CPU split. Empty = no model resident.
```

### Pull (download) a new model

```bash
ollama pull qwen3-coder:30b
```

This downloads outside the launchd-managed list; the model exists locally
but the launchd wrapper won't auto-pull it again on restart unless you
add it to `loadModels`.

### Remove a model

```bash
ollama rm qwen2.5-coder:14b
```

Frees the model blob from `~/.ollama/models/`. If the model is in your
declared `loadModels`, the launchd wrapper will re-pull it on next
restart — remove it from the Nix config too if you want it gone for
good (see **Configuration changes** below).

### Inspect a model

```bash
ollama show qwen3:8b
# Architecture, parameters, context length, quantization, capabilities,
# license, system prompt template.
```

### Run interactively (REPL)

```bash
ollama run qwen3:8b
>>> Write a regex that matches a UUID v4.
>>> /bye      # exit
>>> /?        # REPL command help
```

### One-shot prompt (script-friendly)

```bash
ollama run qwen3:8b "Return only valid JSON: {\"x\": 1}" </dev/null
```

`</dev/null` prevents `ollama run` from waiting on stdin.

## Configuration Changes

The Nix module is the source of truth for which models the launchd wrapper
preloads. Manual `ollama pull`s persist on disk but are not declarative.

### Add a model to auto-preload

Edit your machine config (e.g.
`your-flake/machines/your-host/default.nix`):

```nix
home-manager.users.phillipg.phillipgreenii.programs.ollama.loadModels = [
  "qwen3:8b"
  "qwen3-coder:30b"
];
```

Then `darwin-rebuild switch` and restart the agent.

### Change the default model used by OpenCode

```nix
home-manager.users.phillipg.phillipgreenii.programs.opencode.model =
  "ollama/qwen3-coder:30b";
```

The model name must match an entry in `programs.ollama.loadModels`
(opencode's provider config is auto-built from that list).

### Disable the stack

```nix
home-manager.users.phillipg.phillipgreenii = {
  programs.ollama.enable   = false;
  programs.opencode.enable = false;
};
```

`darwin-rebuild switch` removes the launchd agent and the
`opencode/config.json`. Models persist in `~/.ollama/`; remove manually:

```bash
rm -rf ~/.ollama
```

## OpenCode CLI

```bash
opencode                    # TUI in current dir
opencode run "prompt"       # one-shot, no TUI
opencode models ollama      # list configured ollama models
opencode --version          # version
```

Config lives at `~/.config/opencode/config.json` (Nix-managed; do not
edit by hand).

## Logs and State

| Path                                                 | Contents                                     |
| ---------------------------------------------------- | -------------------------------------------- |
| `~/Library/Logs/ollama.out.log`                      | Daemon stdout (model pulls, request log)     |
| `~/Library/Logs/ollama.err.log`                      | Daemon stderr (Metal init, layer offload)    |
| `~/.ollama/models/`                                  | Model blobs                                  |
| `~/Library/LaunchAgents/phillipgreenii.ollama.plist` | Generated launchd plist                      |
| `~/.config/opencode/config.json`                     | OpenCode provider/model config (Nix-managed) |
| `~/.local/share/opencode/`                           | OpenCode session history                     |

## Common Diagnostics

```bash
# Live tail
tail -f ~/Library/Logs/ollama.err.log

# What env did the daemon launch with?
launchctl print gui/$UID/phillipgreenii.ollama | grep -E "^[[:space:]]+OLLAMA_"

# What model arg is the wrapper using?
grep -A5 ProgramArguments ~/Library/LaunchAgents/phillipgreenii.ollama.plist

# Is it on GPU?
ollama ps   # PROCESSOR column should read "100% GPU" on M3
```

## See Also

- [local-ai-stack-getting-started.md](./local-ai-stack-getting-started.md)
  — first-time setup, performance troubleshooting
- [ADR 0046](../adr/0046-local-ai-coding-stack-ollama-and-opencode.md)
  — design rationale
