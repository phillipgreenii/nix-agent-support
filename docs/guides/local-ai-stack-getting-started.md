# Getting Started with the Local AI Coding Stack

This guide walks through the local AI coding stack added by ADR 0046:
[Ollama](https://ollama.com) (inference daemon) + [OpenCode](https://opencode.ai)
(coding agent) running entirely on the host. Both modules live under
`home/programs/{ollama,opencode}/` in `phillipgreenii-nix-agent-support` and are enabled by the
consuming machine config.

The intended workflow:

- Ollama runs as a launchd user agent at `127.0.0.1:11434`, serving a
  declared list of models (default: `qwen2.5-coder:14b`).
- OpenCode is a TUI; its config at `~/.config/opencode/config.json` points
  at the local Ollama daemon via an OpenAI-compatible provider.
- Claude Code is unaffected and keeps using the Anthropic API.

## Before You Start

After `darwin-rebuild switch --flake .`, confirm the launchd agent is up
and the model is loaded.

```bash
# 1. Launchd registration
launchctl list | grep ollama
# Expect a row for phillipgreenii.ollama with a numeric PID.

# 2. Daemon HTTP responsive
curl -fsS http://127.0.0.1:11434/api/tags | jq '.models[].name'
# Expect: "qwen2.5-coder:14b" (after first-pull completes).
```

The first launchd start triggers `ollama pull qwen2.5-coder:14b`
(~9 GB). Subsequent restarts are no-ops because the wrapper checks
`ollama list` first. Watch progress:

```bash
tail -f ~/Library/Logs/ollama.out.log
```

You'll see `pulling manifest`, `pulling <hash>`, then `success`. When
the log goes quiet the model is ready.

## Level 1: Talk to the Model Directly (Day 1)

Confirm the Ollama runtime works before adding any agent on top.

### One-shot prompt

```bash
ollama run qwen2.5-coder:14b "Write a Python one-liner that reverses a string."
```

First-token latency on an M3 Pro: ~5–20 seconds while Metal warms. Then
tokens stream at 15–30 tok/s when running on GPU. If you see >1 minute
to first token, jump to **Performance Troubleshooting** below.

### Interactive REPL

```bash
ollama run qwen2.5-coder:14b
>>> Write a bash function that prints all files in $1 modified in the last hour.
>>> /bye
```

`/bye` exits the REPL. `/?` shows REPL commands.

### Inspect what's loaded

```bash
ollama list                     # installed models
ollama ps                       # currently in memory; PROCESSOR column = CPU/GPU split
ollama show qwen2.5-coder:14b   # parameters, context length, quantization
```

`ollama ps` is the fastest health check. If `PROCESSOR` shows `100% GPU`,
Metal is engaged. If `100% CPU`, you're running on CPU and inference
will be 10–30× slower — see **Performance Troubleshooting**.

## Level 2: Use OpenCode (Day 2)

OpenCode is a coding-focused TUI that routes prompts through the local
Ollama daemon by default.

### Verify config

```bash
cat ~/.config/opencode/config.json
```

Expect a JSON object with `provider.ollama` block, `model` set to
`ollama/qwen2.5-coder:14b`, and `autoshare: false`. The provider block
is auto-generated from `phillipgreenii.programs.ollama.{host,port,loadModels}`,
so the two stay in sync.

### Confirm OpenCode resolves the provider

```bash
opencode models ollama
```

Expect a list including `qwen2.5-coder:14b`. If it returns
`Error: Provider not found: ollama`, the config didn't activate — re-run
`darwin-rebuild switch` and verify `~/.config/opencode/config.json` was
rewritten.

### One-shot prompt

```bash
opencode run "summarize the difference between CPS and trampolined recursion"
```

Useful for piping into other tools. Same model, no TUI.

### Interactive TUI

```bash
cd /some/code/project
opencode
```

The TUI launches in the current directory; OpenCode reads the file
tree as context. Type prompts, watch tokens stream, exit via `Ctrl+C`
or its own menu.

Common TUI commands (varies by version — check `:help` inside the TUI):

- `:quit` — exit
- `:model` — switch model (must be in your provider block)
- `:clear` — clear session
- `:save` / `:load` — session persistence

### What about Claude Code?

Claude Code is independent. It still uses the Anthropic API and the
`claude-code` binary. The local stack is purely local — no network
calls leave the machine.

## Performance Troubleshooting

### Symptom: 1-minute+ to first token, or full minutes between tokens

```bash
ollama ps
```

If `PROCESSOR` shows any `CPU`, Metal isn't engaging the model.
Check the load log:

```bash
grep -E "offloaded|GPU|metal" ~/Library/Logs/ollama.err.log | tail -20
```

Look for `load_tensors: offloaded 0/N layers to GPU`. That means
Ollama's auto-detection decided it didn't have enough unified RAM
for full offload and fell back to CPU.

**Mitigations:**

1. **Free RAM and reload.** Quit RAM-hungry apps (browsers, Slack,
   Docker, IntelliJ) and kick the daemon:

   ```bash
   launchctl kickstart -k gui/$UID/phillipgreenii.ollama
   ```

   Then re-run a prompt; re-check `ollama ps`.

2. **Force max GPU layers** via the module's `extraEnv` option:

   ```nix
   phillipgreenii.programs.ollama.extraEnv = {
     OLLAMA_NUM_GPU = "999";          # offload all layers
     OLLAMA_KEEP_ALIVE = "24h";       # keep model resident
     OLLAMA_FLASH_ATTENTION = "1";    # ~10-20% speedup on Apple Silicon
   };
   ```

   Then `darwin-rebuild switch`.

3. **Switch to a smaller model.** `qwen2.5-coder:7b` is ~4.7 GB and
   fits comfortably alongside browsers/IDEs. Quality drop on coding
   tasks is small.

   ```nix
   phillipgreenii.programs.ollama.loadModels = [ "qwen2.5-coder:7b" ];
   phillipgreenii.programs.opencode.model    = "ollama/qwen2.5-coder:7b";
   ```

### Symptom: Model gets unloaded between prompts

Default `OLLAMA_KEEP_ALIVE` is 5 minutes. Each prompt after that pays
the 8 GB reload cost. Set `OLLAMA_KEEP_ALIVE=24h` (or `-1` for
forever) via `extraEnv` as above.

### Symptom: launchd throttles inference under load

`ProcessType = "Background"` in the launchd plist tells macOS this is a
batch worker. Apple may de-prioritize CPU/GPU scheduling. For an
interactive coding workload, `Adaptive` is more appropriate. (Future
revision of the module will likely flip this default.)

## Configuration

### Adding a second model

```nix
phillipgreenii.programs.ollama.loadModels = [
  "qwen2.5-coder:14b"
  "qwen2.5-coder:32b"   # ~20 GB; close other apps before first pull
];
```

After `darwin-rebuild switch`, the launchd wrapper will pull the new
model on its next start. The opencode `provider.ollama.models` block
auto-includes anything in `loadModels`.

### Switching the default OpenCode model

```nix
phillipgreenii.programs.opencode.model = "ollama/qwen2.5-coder:32b";
```

### Pointing OpenCode at a remote Ollama

Override the `providers` option entirely (or set
`phillipgreenii.programs.ollama.host`/`.port` if you've bound the
local daemon non-default).

### Disabling

```nix
phillipgreenii.programs.ollama.enable   = false;
phillipgreenii.programs.opencode.enable = false;
```

`darwin-rebuild switch` removes the launchd agent and the
`opencode/config.json`. Models persist in `~/.ollama/`; remove
manually to reclaim disk:

```bash
rm -rf ~/.ollama
```

## Logs and State

| Path                             | Contents                                          |
| -------------------------------- | ------------------------------------------------- |
| `~/Library/Logs/ollama.out.log`  | Daemon stdout (model pulls, request handling)     |
| `~/Library/Logs/ollama.err.log`  | Daemon stderr (Metal init, layer offload, errors) |
| `~/.ollama/models/`              | Model blobs (~9 GB per 14b model)                 |
| `~/.config/opencode/config.json` | OpenCode configuration (Nix-managed)              |
| `~/.local/share/opencode/`       | OpenCode session history                          |

## Quick Command Reference

```bash
# Ollama
ollama list                                      # installed models
ollama ps                                        # currently loaded; check GPU column
ollama run <model> "prompt"                      # one-shot
ollama pull <model>                              # add a model
ollama rm <model>                                # delete
launchctl kickstart -k gui/$UID/phillipgreenii.ollama  # restart daemon

# OpenCode
opencode models ollama                           # confirm provider wired
opencode run "prompt"                            # one-shot
opencode                                         # TUI in cwd
opencode --version                               # version info

# Diagnostics
curl -fsS http://127.0.0.1:11434/api/tags | jq   # daemon health + models
tail -f ~/Library/Logs/ollama.err.log            # live error log
```

## Related Reading

- [ADR 0046](../adr/0046-local-ai-coding-stack-ollama-and-opencode.md) — design rationale
- [ADR 0011](../adr/0011-colocated-options-pattern-one-module-per-program.md) — module pattern
- [ADR 0029](../adr/0029-launchd-service-activation-pattern-via-home-manager.md) — launchd via home-manager
- Plan: [`docs/superpowers/plans/2026-04-29-local-ai-coding-stack.md`](../superpowers/plans/2026-04-29-local-ai-coding-stack.md)
