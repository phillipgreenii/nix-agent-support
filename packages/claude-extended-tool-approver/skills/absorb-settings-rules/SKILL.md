---
name: absorb-settings-rules
description: Identify settings.local.json permission rules that should be absorbed into Go rule modules and create beads for each. Use to find permission rules in settings.local.json that should become Go rule modules, reducing reliance on per-user settings.
---

# Absorb Settings Rules

Analyze settings.local.json to find permission workarounds that should be absorbed into the Go rule engine. Present the rules for user review, then create beads for approved absorptions.

**This skill is read-only triage.** It does NOT fix code, modify the DB, or change settings. It creates beads for later deep-dive sessions.

**Target time: 5-10 minutes (longer if jq debugging is needed).**

## Terminology

- `hook_decision` — the decision the hook returned (`APPROVE`/`ASK`/`DENY`/`ABSTAIN`); from the `hook_decision` field of `evaluate`/`show` output.
- `settings_decision` — the decision `settings.local.json` rules would have returned for this row; from the `settings_result` field of `evaluate` (only present when `--settings=<path>` is used).
- `category` — `miss-uncaught`, `miss-caught-by-settings`, `correct`, `needs-review`, `stale-cwd`; from the `category` field.
- `outcome` — the user's actual decision (ground truth); from the `outcome` field.
- `sandbox_enabled` — `0` or `1` (or `null`) indicating whether the OS bash sandbox was active for this invocation; from the `sandbox_enabled` field.

Full schema: [../references/database-schema.md](../references/database-schema.md). Shared definitions: [../references/terminology.md](../references/terminology.md).

## Phase 1: Identify (No Approvals Needed)

All commands in this phase use `claude-extended-tool-approver`, `jq`, or file reads — no raw sqlite3, no file modifications.

### Step 1: Find settings.local.json files

Check common locations:

```bash
ls -la .claude/settings.local.json ~/.claude/settings.local.json 2>/dev/null
```

If multiple exist, present both paths and ask the user which to analyze. If only one exists, use it. If none exist, report "No settings.local.json found" and stop.

### Step 2: Evaluate with settings

```bash
claude-extended-tool-approver evaluate \
  --settings=<path-to-settings.local.json> \
  --format=json > /tmp/ceta-settings-eval.json
```

### Step 3: Filter for settings-caught misses

```bash
jq '[.[] | select(.category == "miss-caught-by-settings")]' \
  /tmp/ceta-settings-eval.json > /tmp/ceta-settings-misses.json
jq 'length' /tmp/ceta-settings-misses.json
```

If zero, report "No settings rules to absorb — the hook covers everything" and stop.

### Step 4: Group by pattern and rank

```bash
jq 'group_by(.tool_summary) | map({
  pattern: .[0].tool_summary,
  tool_name: .[0].tool_name,
  count: length,
  ids: [.[].id],
  sample_ids: [.[].id][0:3],
  sandbox: ([.[].sandbox_enabled] | group_by(.) | map({k: (.[0] // "unknown"), n: length}))
}) | sort_by(-.count)' /tmp/ceta-settings-misses.json
```

**Prioritize `sandbox_enabled=1` rows.** See [../references/sandbox-enabled.md](../references/sandbox-enabled.md) for prioritization logic.

### Step 5: Cross-reference with settings.local.json rules

Read the settings file and identify which `allow`/`deny` rules correspond to each pattern group.

#### 5.1 Extract rules with jq

```bash
jq '.permissions.allow' <path-to-settings.local.json>
jq '.permissions.deny'  <path-to-settings.local.json>
```

To list all rules together:

```bash
jq '{allow: .permissions.allow, deny: .permissions.deny}' <path-to-settings.local.json>
```

Settings rules are tool-name + pattern matchers of the form `ToolName(pattern)`, e.g. `Bash(rg:*)`, `Read(./src/**)`, `WebFetch(domain:github.com)`. The portion before `(` is the exact tool name (`Bash`, `Read`, etc.); the portion inside the parentheses is a tool-specific matcher — for `Bash` it is a command-prefix match where `:*` allows any trailing arguments, for file tools it is a glob, and for `WebFetch` it is a `domain:<host>` matcher. A row matches a rule when its `tool_name` matches and the rule's matcher matches its invocation.

#### 5.2 Edge case: settings rules with no overlap

It is possible for `settings.local.json` to contain rules that never appear as `miss-caught-by-settings` (rules covering tools that simply never ran, or tools the hook already handles correctly). These are orthogonal to absorption — there is nothing to absorb. If Step 3 returned zero rows but the settings file contains rules, report "settings rules exist but none caught any hook misses — nothing to absorb" and stop.

#### 5.3 Pick the target rule module

For each pattern, choose the closest existing module under `internal/rules/`. Current modules:

| Module        | Use for                                                                  |
| ------------- | ------------------------------------------------------------------------ |
| `assume`      | AWS `assume`/STS-related credential operations.                          |
| `buildtools`  | Generic build tooling (`make`, `cargo`, language toolchains).            |
| `claudetools` | Claude Code's own tools (`Read`, `Edit`, `Glob`, `Grep`, etc.).          |
| `curl`        | `curl` invocations.                                                      |
| `docker`      | `docker`/container CLI invocations.                                      |
| `envvars`     | Environment variable inspection / setting.                               |
| `gh`          | GitHub CLI (`gh`).                                                       |
| `git`         | Git CLI.                                                                 |
| `kubectl`     | Kubernetes CLI.                                                          |
| `mcp`         | MCP server tool invocations.                                             |
| `monorepo`    | Monorepo-aware path/scope rules.                                         |
| `nix`         | Nix CLI (`nix`, `nix-build`, `nix-shell`, etc.).                         |
| `pathsafety`  | Path traversal / writable-directory safety checks.                       |
| `safecmds`    | Generic safe read-only Bash commands (`ls`, `cat`, `head`, `pwd`, etc.). |
| `sqlite3`     | `sqlite3` CLI.                                                           |
| `webfetch`    | `WebFetch` tool / URL fetching.                                          |
| `znself`      | `zn-self-*` user commands.                                               |

If no existing module fits, propose a new module name in the bead.

### Step 6: Get sample rows for each group

```bash
claude-extended-tool-approver show <sample_id_1> <sample_id_2> --format=json
```

**Tip:** If tracing was enabled (`CLAUDE_TOOL_APPROVER_TRACE=1`) when the hook ran, the `show` output includes a `trace` array showing every rule that was evaluated, its decision, and reason. This reveals which rule module to target for absorption.

### Step 7: Present findings to user

Present exactly this format:

```text
## Settings Rules to Absorb (ranked by coverage)

Settings file: <path>

1. **`<settings pattern>` — covers <N> rows** (sandbox: on=<X> off=<Y> unknown=<Z>)
   Target module: internal/rules/<module>/
   Sample: rows <id1>, <id2>, <id3>
   Hook currently: <hook_decision> | Settings says: <settings_decision>

2. ...

Total: <M> settings rules covering <T> rows that the hook should handle natively.

Which rules should I create beads for?
```

**CRITICAL:** Wait for explicit user approval before proceeding to Phase 2.

## Phase 2: Create Beads (After User Approval)

For each user-approved rule, create a bead. Both pattern-analysis skills create `task` beads since they propose improvements rather than fix defects:

```bash
bd create \
  --title="Absorb setting: <settings pattern>" \
  --description="<see template below>" \
  --type=task \
  --priority=2
```

Then label it:

```bash
bd label add <bead-id> claude-extended-tool-approver
```

### Bead Description Template

Use this exact template, filling in the values from Phase 1 data:

```markdown
## Problem

The settings.local.json rule `<settings-pattern>` is handling <N> tool
decisions that should be covered by the Go rule engine.

## Evidence

- **Settings rule:** `<pattern from settings.local.json>`
- **Settings file:** `<path-to-settings.local.json>`
- **Row count:** <N>
- **Row IDs:** <id1>, <id2>, ..., <idN>
- **Target rule module:** `internal/rules/<module>/`
- **Sample rows (from show):**
  - ID <id1>: `<tool_summary>` — hook=<hook_decision>, settings=<settings_decision>
  - ID <id2>: `<tool_summary>` — hook=<hook_decision>, settings=<settings_decision>

## Reproduce

claude-extended-tool-approver evaluate \
 --settings=<path> --format=json | \
 jq '[.[] | select(.category == "miss-caught-by-settings" and (.tool_summary | test("<pattern>")))]'

claude-extended-tool-approver show <id1> <id2> --format=json

## Debugging

If trace data is available (from `CLAUDE_TOOL_APPROVER_TRACE=1`), inspect the rule chain:

claude-extended-tool-approver show <id1> --format=json | jq '.[] | .trace'

This shows which rules evaluated and why each abstained, helping identify the target module.

## Acceptance Criteria

- [ ] Go rule module handles this pattern correctly
- [ ] settings.local.json rule removed
- [ ] evaluate passes (no regressions)
- [ ] go test ./... passes
```

## Constraints

- **Phase 1 MUST NOT modify anything** — no files, no DB, no `settings.local.json`, no approvals required.
- **MUST wait for explicit user approval before Phase 2.**

Beads should include the `claude-extended-tool-approver` label and reference row IDs and CLI commands rather than `/tmp` paths (which are intermediate-only). Phase 2 creates beads only — no code changes, no `set-correct-decision`, no `mark-excluded`.

## Key Paths

- Binary: `packages/claude-extended-tool-approver/cmd/claude-extended-tool-approver/`
- Rule modules: `packages/claude-extended-tool-approver/internal/rules/*/`
- Settings evaluator: `packages/claude-extended-tool-approver/internal/settingseval/`
- Database: `~/.local/share/claude-extended-tool-approver/asks.db`
- Trace env var: `CLAUDE_TOOL_APPROVER_TRACE=1` — enables per-rule decision tracing in `show` output
