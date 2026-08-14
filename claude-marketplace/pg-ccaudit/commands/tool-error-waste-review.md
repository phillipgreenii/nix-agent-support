---
description: Run a tool-error waste review over the indexed Claude Code transcript corpus (queries the pg-ccaudit SQLite index; never scans raw JSONL)
argument-hint: "[--since YYYY-MM-DD] [--until YYYY-MM-DD]"
---

# Tool-error waste review

**FIRST INSTRUCTION: THE DATABASE ALREADY EXISTS. QUERY IT.**

**You MUST NOT scan the raw transcripts** under `~/.claude/projects` — not with
`Read`, not with `Grep`, not with `Glob`, not with `jq`. The corpus is ~1.7 GiB
and scanning it raw stalled a supervising agent's progress watchdog twice; that is
the failure this tooling exists to prevent. Everything you need is already
indexed.

Invoke the `tool-error-waste-review` skill and follow it. It carries the full
method: the canned query set, the runaway discount, the measured-cost caveat, and
the per-class main-loop/subagent routing table.

Window for this run: `$ARGUMENTS` (if empty, ask the operator for a window rather
than silently censusing all of history — an unbounded window is not comparable
with anything).

Start here:

```bash
pg-ccaudit status      # coverage + staleness; read it before anything else
pg-ccaudit queries     # the named, versioned canned queries
```

Then work the four dimensions in the skill, in order, and do not skip
`sidechain-split` — it is what decides whether each fix belongs in the always-on
user rules, in a subagent brief, or in the permission-approver rule set.
