# `sandbox_enabled` Prioritization

Every row in the `evaluate` and `show` output carries a `sandbox_enabled` field:

- `1` — Claude Code's bash sandbox was active when the decision was logged.
- `0` — the sandbox was not active.
- `null` — the row predates sandbox telemetry.

## Why `sandbox_enabled=1` rows are highest value

Rows with `sandbox_enabled=1` are the _residual_: prompts that occurred even though the OS sandbox was already containing filesystem and network damage. They represent permission needs the sandbox cannot reach — semantic argument checks, non-Bash tools, and `dangerouslyDisableSandbox` fallbacks. Absorbing these into Go rule modules has the highest impact.

Rows with `sandbox_enabled=0` may become unnecessary once the sandbox is enabled everywhere; flag those for review rather than absorption.

## Bucket misses by sandbox state

```bash
jq 'group_by(.sandbox_enabled) | map({
  sandbox: (.[0].sandbox_enabled // "unknown"),
  count: length
})' /tmp/ceta-misses.json
```
