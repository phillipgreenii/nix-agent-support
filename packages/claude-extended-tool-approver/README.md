# claude-extended-tool-approver

Claude Code extended tool approval with rule-based permission evaluation and decision logging.

## What it does

Evaluates tool invocations against an ordered chain of rule modules (envvars, git, pathsafety, etc.), returning APPROVE, ASK, DENY, or ABSTAIN. Logs all ASK and DENY decisions plus their outcomes to a SQLite database.

## Hook Events

| Event             | Purpose                                              |
| ----------------- | ---------------------------------------------------- |
| PreToolUse        | Rule engine evaluation + log ask/deny decisions      |
| PermissionRequest | Log all permission dialogs (including built-in ASKs) |
| PostToolUse       | Resolve pending ASKs as approved                     |
| SessionEnd        | Resolve remaining pending ASKs as denied             |

## Decision Database

Stored at `~/.local/share/claude-extended-tool-approver/asks.db` (or `$XDG_DATA_HOME/claude-extended-tool-approver/asks.db`).

### Example Queries

```bash
DB=~/.local/share/claude-extended-tool-approver/asks.db

# Most common ASKs you keep approving (candidates for auto-approve rules)
sqlite3 "$DB" \
  "SELECT tool_name, tool_summary, COUNT(*) n FROM tool_decisions
   WHERE outcome='approved' AND hook_decision='ask'
   GROUP BY tool_name, tool_summary ORDER BY n DESC LIMIT 20"

# Built-in ASKs you keep approving (candidates for NEW hook rules)
sqlite3 "$DB" \
  "SELECT tool_name, tool_summary, COUNT(*) n FROM tool_decisions
   WHERE outcome='approved' AND hook_decision IS NULL
   GROUP BY tool_name, tool_summary ORDER BY n DESC LIMIT 20"

# What is the hook auto-denying? (sanity check deny rules)
sqlite3 "$DB" \
  "SELECT tool_summary, hook_reason, COUNT(*) n FROM tool_decisions
   WHERE hook_decision='deny'
   GROUP BY tool_summary, hook_reason ORDER BY n DESC LIMIT 20"

# All denied ASKs (things you said no to)
sqlite3 "$DB" \
  "SELECT created_at, cwd, tool_name, tool_summary FROM tool_decisions
   WHERE outcome='denied' AND hook_decision!='deny'
   ORDER BY created_at DESC LIMIT 20"

# Which agent types trigger the most ASKs?
sqlite3 "$DB" \
  "SELECT COALESCE(agent_type, 'main') as agent, tool_name, outcome, COUNT(*) n
   FROM tool_decisions
   GROUP BY agent, tool_name, outcome ORDER BY n DESC LIMIT 20"

# Residual prompts while the Claude Code sandbox was active.
# sandbox_enabled: 1 = sandbox on, 0 = sandbox off, NULL = pre-feature (unknown).
# Rows with sandbox_enabled=1 are prompts that happened despite the OS
# sandbox containing filesystem/network damage — the highest-value
# candidates for new rule coverage.
sqlite3 "$DB" \
  "SELECT tool_name, tool_summary, COUNT(*) n FROM tool_decisions
   WHERE sandbox_enabled = 1 AND hook_decision = 'ask' AND outcome = 'approved'
   GROUP BY tool_name, tool_summary ORDER BY n DESC LIMIT 20"

# Overall prompt volume by sandbox state (is sandbox helping?)
sqlite3 "$DB" \
  "SELECT COALESCE(sandbox_enabled, 'unknown') AS sandbox,
          COUNT(*) AS total,
          SUM(CASE WHEN outcome='approved' AND hook_decision IS NULL THEN 1 ELSE 0 END) AS builtin_asks_approved
   FROM tool_decisions
   GROUP BY sandbox_enabled"

# Recent decisions for a specific worktree
sqlite3 "$DB" \
  "SELECT created_at, tool_name, tool_summary, hook_decision, outcome
   FROM tool_decisions WHERE cwd LIKE '%my-project%'
   ORDER BY created_at DESC LIMIT 30"

# Full detail for a session
sqlite3 "$DB" -header -column \
  "SELECT created_at, tool_name, tool_summary, hook_decision, outcome
   FROM tool_decisions WHERE session_id='SESSION_ID_HERE'
   ORDER BY created_at"
```

## Rule Modules

Rules are evaluated in order; first non-ABSTAIN wins:

1. **envvars** -- dangerous environment variables
2. **webfetch** -- WebFetch to allowed hosts
3. **claudetools** -- AskQuestion, Glob, Grep, etc.
4. **pathsafety** -- file operations with path-based policies
5. **mcp** -- MCP tool allowlist
6. **git** -- git subcommands
7. **gh** -- GitHub CLI
8. **monorepo** -- monorepo bin commands
9. **safecmds** -- safe commands with path checks
10. **curl** -- read-only curl to allowed domains
11. **kubectl** -- Kubernetes operations
12. **buildtools** -- gradle, pre-commit, bats, etc.

## Dependencies

Go deps are not vendored. The Nix build uses `vendorHash` to fetch modules reproducibly. After changing Go dependencies (adding/removing imports, `go get -u`, etc.), refresh the hash:

```bash
./update-deps.sh
```

Or refresh everything at once via the workspace-level `../../update-locks.sh`. See [ADR 0035](../../docs/adr/0035-vendor-hash-with-nix-update-for-go-packages.md) for background.
