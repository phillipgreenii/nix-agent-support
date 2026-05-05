---
name: identify-hook-misses
description: Identify the 2-5 most impactful hook miss patterns from the decision database and create beads for each. Use after a week+ of normal Claude Code usage to surface high-impact hook decision patterns worth fixing.
---

# Identify Hook Misses

Analyze the decision database to find patterns where the hook makes wrong decisions. Present the top 2-5 most impactful miss patterns for user review, then create beads for approved patterns.

**This skill is read-only triage.** It does NOT fix code, modify the DB, or change settings. It creates beads for later deep-dive sessions.

**Target time: 5-10 minutes (longer if jq debugging is needed).**

## Terminology

- `hook_decision` — the decision the hook returned (`APPROVE`/`ASK`/`DENY`/`ABSTAIN`); from the `hook_decision` field of `evaluate`/`show` output.
- `category` — `miss-uncaught`, `miss-caught-by-settings`, `correct`, `needs-review`, `stale-cwd`; from the `category` field of `evaluate`.
- `outcome` — the user's actual decision (ground truth); from the `outcome` field.
- `sandbox_enabled` — `0` or `1` (or `null`) indicating whether the OS bash sandbox was active for this invocation; from the `sandbox_enabled` field.

Full schema: [../references/database-schema.md](../references/database-schema.md). Shared definitions: [../references/terminology.md](../references/terminology.md).

## Phase 1: Identify (No Approvals Needed)

All commands in this phase use `claude-extended-tool-approver` or `jq` — no raw sqlite3, no file modifications.

### Step 1: Get all misses

```bash
claude-extended-tool-approver evaluate --misses-only --format=json > /tmp/ceta-misses.json
```

Check how many misses exist:

```bash
jq 'length' /tmp/ceta-misses.json
```

If zero misses, report "No misses found" and stop.

### Step 2: Group by pattern and rank

```bash
jq 'group_by(.tool_summary) | map({
  pattern: .[0].tool_summary,
  tool_name: .[0].tool_name,
  count: length,
  ids: [.[].id],
  sample_ids: [.[].id][0:3],
  categories: ([.[].category] | unique),
  sandbox: ([.[].sandbox_enabled] | group_by(.) | map({k: (.[0] // "unknown"), n: length}))
}) | sort_by(-.count) | .[0:10]' /tmp/ceta-misses.json
```

**Prioritize `sandbox_enabled=1` misses.** See [../references/sandbox-enabled.md](../references/sandbox-enabled.md) for prioritization logic.

### Step 3: Get sample rows for top groups

For the top 5-10 pattern groups, get full details on 2-3 sample rows per group:

```bash
claude-extended-tool-approver show <sample_id_1> <sample_id_2> <sample_id_3> --format=json
```

**Tip:** If tracing was enabled (`CLAUDE_TOOL_APPROVER_TRACE=1`), the `show` output includes a `trace` array showing every rule that was evaluated, its decision, and reason. This reveals _why_ each rule abstained — invaluable for deciding which rule module to modify.

### Step 4: Present ranked findings to user

Present exactly this format:

```text
## Hook Miss Patterns (ranked by frequency)

1. **`<tool_summary pattern>` — <N> misses** (sandbox: on=<X> off=<Y> unknown=<Z>)
   Hook says: <hook_decision> | Expected: <expected from outcome>
   Categories: <miss-uncaught, miss-caught-by-settings, etc.>
   Sample rows: <id1>, <id2>, <id3>

2. ...

I recommend creating beads for patterns 1-<M> (the top <M> by count).
Which patterns should I create beads for?
```

**CRITICAL:** Wait for explicit user approval before proceeding to Phase 2.

## Phase 2: Create Beads (After User Approval)

For each user-approved pattern, create a bead. Both pattern-analysis skills create `task` beads since they propose improvements rather than fix defects:

```bash
bd create \
  --title="Hook miss: <pattern description>" \
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

The hook currently returns `<hook_decision>` for `<pattern>` commands, but the
expected decision is `<expected_decision>` (based on user outcome: <outcome>).

## Evidence

- **Miss count:** <N> rows
- **Row IDs:** <id1>, <id2>, ..., <idN>
- **Sample rows (from show):**
  - ID <id1>: `<tool_summary>` — hook=<hook_decision>, outcome=<outcome>
  - ID <id2>: `<tool_summary>` — hook=<hook_decision>, outcome=<outcome>
  - ID <id3>: `<tool_summary>` — hook=<hook_decision>, outcome=<outcome>

## Reproduce

claude-extended-tool-approver evaluate --misses-only --format=json | \
 jq '[.[] | select(.tool_summary | test("<pattern-regex>"))]'

claude-extended-tool-approver show <id1> <id2> <id3> --format=json

## Debugging

If `CLAUDE_TOOL_APPROVER_TRACE=1` was set when the hook originally ran, the `show` output includes a `trace` array with the full rule evaluation chain — every rule, its decision, and reason. Use this to see exactly why each rule abstained:

claude-extended-tool-approver show <id1> --format=json | jq '.[] | .trace'

If trace data is not available (tracing was off), enable it for future sessions:

export CLAUDE_TOOL_APPROVER_TRACE=1

## Acceptance Criteria

- [ ] Pattern and target rule module identified
- [ ] Tracking ticket filed for the implementation work

This bead covers identifying the pattern and filing a tracking ticket; implementation (modifying the Go rule module, adding tests, running `set-correct-decision` on resolved rows) is a separate ticket.
```

## Constraints

- **Phase 1 MUST NOT modify anything** — no files, no DB, no `settings.local.json`, no approvals required.
- **MUST wait for explicit user approval before Phase 2.**

Beads should include the `claude-extended-tool-approver` label, target 2-5 improvements per run focused on the highest-impact patterns, and reference row IDs and CLI commands rather than `/tmp` paths (which are intermediate-only). Phase 2 creates beads only — no code changes, no `set-correct-decision`, no `mark-excluded`.

## Key Paths

- Binary: `packages/claude-extended-tool-approver/cmd/claude-extended-tool-approver/`
- Rule modules: `packages/claude-extended-tool-approver/internal/rules/*/`
- Database: `~/.local/share/claude-extended-tool-approver/asks.db`
- Trace env var: `CLAUDE_TOOL_APPROVER_TRACE=1` — enables per-rule decision tracing in `show` output
