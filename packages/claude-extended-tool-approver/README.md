# claude-extended-tool-approver

Claude Code extended tool approval with rule-based permission evaluation and decision logging.

## What it does

Evaluates tool invocations against an ordered chain of rule modules (envvars, git, pathsafety, etc.), returning APPROVE, ASK, DENY, or ABSTAIN. Logs all ASK and DENY decisions plus their outcomes to a SQLite database.

## Hook Events

| Event             | Purpose                                                       |
| ----------------- | ------------------------------------------------------------- |
| PreToolUse        | Rule engine evaluation + log ask/deny decisions               |
| PermissionRequest | Log all permission dialogs (including built-in ASKs)          |
| PostToolUse       | Resolve pending ASKs as approved                              |
| PermissionDenied  | Resolve as denied — the call was actually declined            |
| SessionEnd        | Resolve remaining pending ASKs as **unresolved** (not denied) |

## Decision Database

Stored at `~/.local/share/claude-extended-tool-approver/asks.db` (or `$XDG_DATA_HOME/claude-extended-tool-approver/asks.db`).

### Outcome values

`outcome` is the ground truth `evaluate` grades hook correctness against, so each
value names exactly ONE provenance. Three of the five are refusal-shaped and MUST
NOT be conflated — a bulk `SessionEnd` sweep is not a user saying no.

| `outcome`    | Meaning                                                                                                        | Written by               | Gradeable? |
| ------------ | -------------------------------------------------------------------------------------------------------------- | ------------------------ | ---------- |
| `pending`    | Logged at PreToolUse, not resolved yet. Live sessions only.                                                    | `RecordPreToolDecision`  | no         |
| `approved`   | PostToolUse fired — the tool actually ran.                                                                     | `ResolveApproved`        | yes        |
| `denied`     | A decline **judgement** was rendered, by the user or the auto-mode classifier. Always carries `outcome_notes`. | `RecordPermissionDenied` | yes        |
| `rejected`   | CETA itself returned Reject. Nobody was asked; `hook_decision='deny'` and `resolved_at == created_at`.         | `RecordPreToolDecision`  | yes        |
| `unresolved` | Never resolved at all — interrupted, abandoned, session died, agent moved on. Swept in bulk at SessionEnd.     | `ResolveUnresolvedAll`   | **no**     |

`unresolved` rows carry no decision, so `evaluate`, `compare` and `report
--misses-only` never count them as correct or as a miss (`evaluate` reports them
under its own `unresolved` category). Before this split all three refusal shapes
were stored as `denied`, which made every SessionEnd sweep look like "the user
denied it but the hook allows it" — a phantom false-allow. Schema migration 7
backfills historical rows by inverting the three writers' fingerprints.

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

# All declined ASKs (things you said no to). outcome='denied' now means exactly
# that, so no hook_decision filter is needed — a hook Reject is 'rejected' and a
# never-answered call is 'unresolved'.
sqlite3 "$DB" \
  "SELECT created_at, cwd, tool_name, tool_summary FROM tool_decisions
   WHERE outcome='denied'
   ORDER BY created_at DESC LIMIT 20"

# Calls that were never resolved (NOT denials) — abandoned/interrupted work.
sqlite3 "$DB" \
  "SELECT created_at, cwd, tool_name, tool_summary FROM tool_decisions
   WHERE outcome='unresolved'
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

Rules are evaluated in order; first non-ABSTAIN wins (Bash compounds fold most-restrictive-wins).

`internal/setup.RuleChain` is the single source of truth for this list and its order.
Register a new rule **there and nowhere else**: the engine integration suite
(`internal/engine/engine_integration_test.go`) derives its chain from that same
function, so a new rule is automatically exercised by every integration case in its
production band. A rule registered only in a hand-maintained test list is invisible
to the integration suite, which is how `git-directory` shipped non-overridable hard
Rejects with unit coverage only. The list below is documentation and can lag; the code
is authoritative.

1. **config-rules** -- consumer `rules.json` basename allow/block; an `approvedCommands` Approve is argument-blind and absolute for its leaf, so the whole early validator band below is skipped for that leaf (ADR 0040 -- see **approvedCommands** under "Consumer configuration" for the blast radius)
2. **git-directory** -- Reject any read/write inside a `.git/` directory (Bash + file/search tools)
3. **dangerous-commands** -- blanket Reject of inherently dangerous commands (`sudo`, `su`, `doas`, `dd`, `mkfs*`, `fdisk`, `parted`, `mount`, `umount`, `reboot`, `shutdown`, `halt`, `poweroff`, `wget`, `nc`/`ncat`/`netcat`, `telnet`, `sftp`)
4. **path-traversal** -- Ask (human-in-the-loop) on a Bash command containing a `../..` traversal escape
5. **secrets** -- prompts (ASK) before any tool touches a well-known credential/secret path (`.credentials`, `auth.json`, `secrets/**`, `.ssh/**`, `.env`, `*token*.json`) so such reads are never silently approved. Each path is tested both as NAMED and symlink-RESOLVED, so a link into a credential directory (`~/mykeys/id_rsa -> ~/.ssh/id_rsa`) is caught too, wherever it resolves to; for Bash the resolving pass covers path-shaped arguments only (a bare word is never absolutized into a file in the cwd)
6. **envvars** -- dangerous environment variables
7. **assume** -- Reject AWS `assume` (assume-role)
8. **webfetch** -- WebFetch to allowed hosts
9. **claudetools** -- AskQuestion, Glob, Grep, BashOutput (read-only approve), etc.
10. **killshell** -- KillShell: approve terminating an agent-owned tracked background shell, else Ask
11. **pathsafety** -- file operations with path-based policies; a WRITE to an agent-config or agent-instruction file that sits directly in a `.claude/` directory (`settings.json`, `settings.local.json`, `mcp.json`, `.mcp.json`, or any `*.md`) Abstains instead of approving, so the verdict stays with Claude Code (ADR 0041). Deeper paths -- `.claude/skills/**`, `.claude/plugins/**`, `.claude/projects/**`, `.claude/plans/**` -- and all READS are unaffected
12. **mcp** -- MCP tool allowlist + read-only-verb approval (search/get/list/read/fetch/check); mutating verbs (create/edit/update/delete/…) abstain
13. **primary-commit** -- Reject a `git commit` on the canonical clone's primary branch in an auto-approving (`bypassPermissions`) session; Abstain otherwise.
14. **git** -- git subcommands. `git tag` → Reject. On `git push`, Reject every spelling of a force-push (`-f`, `--force`, a clustered `-f…`, a `+`-prefixed refspec), of a remote-ref delete (`--delete`, `-d`, a `:ref` refspec), and `--mirror`; also Reject `--force-with-lease` when the refspec is CROSS-BRANCH. Same-branch `--force-with-lease` stays Approve (the post-rebase idiom). Reject a push whose DESTINATION is a NETWORK URL given in place of a remote name (`https://`, `http://`, `git://`, `ssh://`, any other `<scheme>://`, scp-like `user@host:path`, and the same URL via `--repo`), since that sends repository contents to an arbitrary host with no `git remote` change to show for it; a LOCAL destination (`/path`, `./path`, `../path`, `~/path`, `sub/dir`, `file://`) is deliberately NOT gated. `git branch -D` and `git reset --hard` / `git clean` Ask. `git rebase --interactive` needs an automated `GIT_SEQUENCE_EDITOR`. "Every spelling" INCLUDES git's unique-prefix ABBREVIATIONS: git's parse-options accepts any unambiguous prefix of a long option, so `--har` IS `--hard` and `--interactiv` IS `--interactive`, and each gated long flag is matched by prefix rather than by exact token. `git config` options keep MEASURED abbreviation minimums instead, because a match there shifts the operand count its read/write discrimination rests on
15. **gh** -- GitHub CLI; `gh pr merge` (immediate) → Reject, `gh pr merge --auto` → Abstain. `gh api` is classified by its EFFECTIVE HTTP METHOD, not by the subcommand: a read (GET/HEAD/OPTIONS) → Approve, a mutating method in any spelling (`-X`/`--method`, glued `-XPUT`, `--method=PUT`, and the POST gh defaults to when `-f`/`-F`/`--field`/`--raw-field`/`--input` is present) → Ask, and a PUT to `/repos/{owner}/{repo}/pulls/{n}/merge` → Reject, mirroring the `gh pr merge` verdict it would otherwise bypass
16. **monorepo** -- config-driven monorepo command/script boundary (`monorepo` block: `approvedCommands` + `dangerousEnvByWrapper`); Abstains until configured
17. **nix** / **docker** -- nix and docker policies (mount-aware inner eval)
18. **curl** -- config-driven curl approval (`curl` block: `allowedDomainSuffixes` read-only + per-domain `domainMethods`); base generic hosts (localhost/loopback, GitHub read hosts) approved read-only even with no config; only ever Approves or Abstains
19. **ssh** -- config-driven ssh/scp classification (`ssh` block: user allowlist / read-only commands / secret-path / password-auth); Abstains until configured
20. **vault** -- config-driven Vault read/write verb split (`vault` block: `readVerbs` → approve, `writeVerbs` → ask); Abstains until configured
21. **safecmds** -- safe commands with path checks (runs AFTER curl/ssh/vault so a configured command-aware leaf is decided by its dedicated rule). A path argument that is DYNAMICALLY EXPANDED (`$VAR`, `${VAR}`, `$(…)`, backtick) is not statically determinable, so the command is NOT approved -- for READS (`cat`/`head`/`tail`/`less`/`more`/`wc`/`diff`/`sort`/`uniq`/`awk`/`jq`/`tq`/`xxd`/`strings`, plus `sed`/`yq`/`gofmt`/`grep`/`rg` reads, `jar tf|xf`, `bash -n`) exactly as for writes (`rm`/`cp`/`mv`/`mkdir`/`touch`/`chmod`/`tee`). Reads were previously exempt, and one variable hop therefore erased the credential deny-list entirely: `cat ~/.ssh/id_rsa` denied while `F=~/.ssh/id_rsa; cat $F` auto-approved. A credential read is an exfiltration primitive, so the read path warrants the refusal at least as much as the write path. Three exceptions, each deliberate: the **browsing** commands (`ls`/`find`/`du`/`stat`/`file`/`lsof`) expose names, sizes and timestamps but never file CONTENT, so a dynamic path there is not gated; the PROGRAM operand of `awk`/`sed`/`jq` is code whose literal `$` (an awk field reference, a sed end-of-line anchor, a jq `--arg` variable) is not a shell expansion, so it is judged only against the bare-expansion shapes (`awk $F` is still refused, `awk '{print $1}' f` is not); and the refusal is a NON-APPROVAL (the call returns to Claude Code's prompt), not a deny -- restoring a deny would require resolving the variable by intra-command dataflow
22. **kubectl** -- Kubernetes operations (`kubectl` block extensions)
23. **buildtools** -- gradle, pre-commit, bats, etc. (`buildtools` block extensions)
24. **sqlite3** -- sqlite3 read/write/DDL classification

### Consumer configuration (`rules.json`)

Consumer-specific policy DATA lives in a single file at
`$XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json` and is loaded once,
then dependency-injected into the generic rules (ADR 0033). The base binary
carries no consumer literals; every structured block is data-only, and an absent
or empty block leaves its rule at the safe base default (the command-aware
`ssh`/`vault`/`curl`/`monorepo` rules Abstain, deferring to Claude). Schema:

```jsonc
{
  // flat basename allow (config-rules rule). ABSOLUTE for its leaf: skips the
  // early security band, git-directory's hard deny included. See below + ADR 0040.
  "approvedCommands": ["..."],
  "blockedCommands": ["..."], // flat basename block (Reject)
  "kubectl": {
    /* aliases, plugin verbs, dev-workspace scope */
  },
  "buildtools": {
    "approvedTools": [],
    "approvedScripts": [],
    "verbScopedApprovals": [], // [{ "tool": "just", "verb": "lint-rules" }]
    // per-tool flags that CONSUME their value, so the verb-scope matcher does not
    // mistake the value for the verb (`just -f <justfile> <verb>`). `:<n>` declares
    // a multi-token flag (`--set:2`); the glued `-f=<v>` form supplies the first
    // value inline. An UNDECLARED value flag stays fail-safe: its value lands in
    // the verb slot and the command Abstains — so leave execution-altering flags
    // (`--shell`, `-c/--command`, `--dotenv-path`) out on purpose.
    "valueFlags": { "just": ["-f", "--justfile", "-d", "--working-directory"] },
  },

  "ssh": {
    "allowedUsers": ["deploy"],
    "readonlyCommands": ["ls", "cat", "systemctl"],
    "readonlySubcommands": { "systemctl": ["status", "is-active"] },
    "secretPathPatterns": ["/etc/shadow", "id_rsa", ".env"],
    "passwordFlagPatterns": ["passwordauthentication=yes"],
  },
  "vault": {
    "readVerbs": ["read", "status", "kv get"],
    "writeVerbs": ["write", "delete", "kv put"],
  },
  "curl": {
    // domain WITHOUT leading dot => apex + subdomains; WITH leading dot => subdomains only.
    "allowedDomainSuffixes": ["nixos.org", ".internal.example"],
    // grant extra (possibly non-read-only) HTTP methods to specific domains.
    "domainMethods": [
      { "domainSuffix": ".internal.example", "methods": ["GET", "POST"] },
    ],
  },
  "monorepo": {
    "approvedCommands": ["tc", "uv"],
    "dangerousEnvByWrapper": { "tc": ["TC_DANGER"] },
  },
}
```

- **approvedCommands** / **blockedCommands**: the two TOP-LEVEL flat basename
  lists, decided by the `config-rules` rule at slot 1 of the chain. A
  `blockedCommands` basename → Reject; an `approvedCommands` basename → Approve.
  (Not to be confused with the `monorepo` block's own nested `approvedCommands`,
  described below.)

  **An `approvedCommands` entry is ABSOLUTE for its leaf: it approves the command,
  arguments included, and the early security band is not consulted for that leaf**
  (ADR 0040). Adding a tool to `approvedCommands` is therefore a security decision
  with a wider blast radius than the option name suggests — it exempts that command
  from secret-path detection, from path-traversal screening, and from
  `git-directory`'s hard deny, the last of which is otherwise **not**
  user-overridable anywhere else in the chain. Nothing about the name hints that it
  disables a non-overridable deny, which is why it is called out here.

  **The bar for entry is "I trust this command with any argument it is handed."** A
  command that takes arbitrary paths, reads files it is pointed at, or forwards its
  operands to another program does not meet that bar and belongs outside the list.

  **Removal is the escalation path.** If an allowlisted command turns out to be a
  problem, pull it from `approvedCommands`; it then falls through to the normal
  chain and its arguments are screened again. No code change is needed to apply
  that mitigation.

  The exemption is per-LEAF, not total, so three backstops survive and must not
  regress: the engine's redirection check (`grazr > /etc/hosts` → Reject), a
  sibling leaf judged on its own (`grazr && sudo rm -rf /` → Reject), and
  `config-rules`' own withhold when the leaf carries inline env assignments
  (`FOO=bar grazr build` → Abstain). See
  [ADR 0040](../../docs/adr/0040-ceta-approved-commands-are-absolute.md) for the
  decision and its full Consequences.

- **ssh**: password-auth patterns / `sshpass` → Reject; a login user outside
  `allowedUsers` → Reject; an interactive session, an unrecognized remote
  command, a redirect/`tee`, or a `secretPathPatterns` match → Ask; a remote
  command whose every segment is a `readonlyCommands` basename (honoring
  `readonlySubcommands`) on a non-secret path → Approve.
- **vault**: `readVerbs` → Approve, `writeVerbs` → Ask (single token or `"a b"`
  compound, compound matched first); anything else → Abstain.
- **curl**: a read-only method (GET/HEAD) to a base host or an
  `allowedDomainSuffixes` domain → Approve; a `domainMethods` domain whose list
  includes the request method → Approve; everything else → Abstain.
- **monorepo**: a `monorepo.approvedCommands` basename — this block's own nested
  key, distinct from the top-level list above — after normalizing the executable
  relative to the project root → Approve, unless it carries an inline
  assignment of one of its `dangerousEnvByWrapper` vars (→ Abstain).

Background-shell tracking: on **PostToolUse** of a `run_in_background` Bash call, the resulting shell id is recorded (SQLite `background_shells` table, `internal/asklog`) so the **killshell** rule can verify ownership.

## Dependencies

Go deps are not vendored. The Nix build uses `vendorHash` to fetch modules reproducibly. After changing Go dependencies (adding/removing imports, `go get -u`, etc.), refresh the hash:

```bash
./update-deps.sh
```

Or refresh everything at once via the workspace-level `../../update-locks.sh`. See [ADR 0035](../../docs/adr/0035-vendor-hash-with-nix-update-for-go-packages.md) for background.
