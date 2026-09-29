---
name: absorb-settings-rules
description: Identify settings.local.json permission rules that reveal a gap in a command's data spec and create spec-authoring beads for each. Use to find permission rules in settings.local.json that a command spec should cover instead, reducing reliance on per-user settings.
---

# Absorb Settings Rules

**Retargeted 2026-09-27 per operator ruling R1** (docket `tc-o14i5.1`): the old Go rule engine
(`internal/rules/*`) is frozen — deletions only, no new fixes. The target architecture retires
per-command Go policy rules in favor of DATA specs (command/path/target specs, P13/P14). This
skill's triage mechanics are unchanged; only what Phase 2 proposes has changed: instead of
"absorb this into the Go rule engine" it now proposes "author/regenerate the command spec for
`<command>` to cover this case."

Analyze settings.local.json to find permission workarounds that reveal a gap in a command's data
spec. Present the rules for user review, then create spec-authoring beads for the approved gaps.

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

**Keep this file.** Once a command spec covers one of these patterns (Phase 2's
spec-authoring work), re-run the SAME command with `--baseline /tmp/ceta-settings-eval.json`
added to see the decision delta in one command, instead of re-running `evaluate` twice and
diffing by hand — see the Acceptance Criteria below.

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

#### 5.3 Pick the target command / command family

For each pattern, identify the specific command (or command family) whose data spec should cover
it. The table below is retained only as a triage aid — it names the command families the OLD,
now-frozen `internal/rules/` modules used to group by, which remains a useful classification even
though the bead now targets a spec for that command, not a Go module:

| Command family | Covers                                                                   |
| -------------- | ------------------------------------------------------------------------ |
| `assume`       | AWS `assume`/STS-related credential operations.                          |
| `buildtools`   | Generic build tooling (`make`, `cargo`, language toolchains).            |
| `claudetools`  | Claude Code's own tools (`Read`, `Edit`, `Glob`, `Grep`, etc.).          |
| `curl`         | `curl` invocations.                                                      |
| `docker`       | `docker`/container CLI invocations.                                      |
| `envvars`      | Environment variable inspection / setting.                               |
| `gh`           | GitHub CLI (`gh`).                                                       |
| `git`          | Git CLI.                                                                 |
| `kubectl`      | Kubernetes CLI.                                                          |
| `mcp`          | MCP server tool invocations.                                             |
| `monorepo`     | Monorepo-aware path/scope rules.                                         |
| `nix`          | Nix CLI (`nix`, `nix-build`, `nix-shell`, etc.).                         |
| `pathsafety`   | Path traversal / writable-directory safety checks.                       |
| `safecmds`     | Generic safe read-only Bash commands (`ls`, `cat`, `head`, `pwd`, etc.). |
| `sqlite3`      | `sqlite3` CLI.                                                           |
| `webfetch`     | `WebFetch` tool / URL fetching.                                          |
| `configrules`  | Consumer-specific approved/blocked commands from XDG config file.        |

If no existing family fits, name the command directly in the bead — the spec-generation skill
(Phase 3) authors one spec per command, not per family.

### Step 6: Get sample rows for each group

```bash
claude-extended-tool-approver show <sample_id_1> <sample_id_2> --format=json
```

**Tip:** If tracing was enabled (`CLAUDE_TOOL_APPROVER_TRACE=1`) when the hook ran, the `show` output includes a `trace` array showing every rule that was evaluated, its decision, and reason. This reveals which command's spec needs to cover the gap.

### Step 7: Present findings to user

Present exactly this format:

```text
## Settings Rules to Absorb (ranked by coverage)

Settings file: <path>

1. **`<settings pattern>` — covers <N> rows** (sandbox: on=<X> off=<Y> unknown=<Z>)
   Target command spec: `<command>`
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
bd label add <bead-id> spec-authoring
```

### Bead Description Template

Use this exact template, filling in the values from Phase 1 data:

```markdown
## Problem

The settings.local.json rule `<settings-pattern>` is handling <N> tool
decisions that a command data spec should cover instead.

## Evidence

- **Settings rule:** `<pattern from settings.local.json>`
- **Settings file:** `<path-to-settings.local.json>`
- **Row count:** <N>
- **Row IDs:** <id1>, <id2>, ..., <idN>
- **Target command spec:** `<command>` (family: `<command family, if applicable>`)
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

This shows which rules evaluated and why each abstained, helping identify which command's spec
needs to cover the gap.

## Acceptance Criteria

- [ ] Command data spec authored/regenerated to cover this pattern (P13/P14 data format, via the
      Phase 3 spec-generation skill once it exists)
- [ ] settings.local.json rule removed
- [ ] `evaluate --settings=<path> --format=json --baseline=<file>` (the file from Step 2, or a
      fresh capture before the change) reports no less-restrictive moves
```

## Constraints

- **Phase 1 MUST NOT modify anything** — no files, no DB, no `settings.local.json`, no approvals required.
- **MUST wait for explicit user approval before Phase 2.**

Beads should include the `claude-extended-tool-approver` and `spec-authoring` labels and reference row IDs and CLI commands rather than `/tmp` paths (which are intermediate-only). Phase 2 creates beads only — no code changes, no `set-correct-decision`, no `mark-excluded`.

## Key Paths

- Binary: `packages/claude-extended-tool-approver/cmd/claude-extended-tool-approver/`
- Rule modules (frozen per R1 — deletions only, no new fixes): `packages/claude-extended-tool-approver/internal/rules/*/`
- Settings evaluator: `packages/claude-extended-tool-approver/internal/settingseval/`
- Database: `~/.local/share/claude-extended-tool-approver/asks.db`
- Trace env var: `CLAUDE_TOOL_APPROVER_TRACE=1` — enables per-rule decision tracing in `show` output
