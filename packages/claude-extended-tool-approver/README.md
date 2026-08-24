# claude-extended-tool-approver

Claude Code extended tool approval with rule-based permission evaluation and decision logging.

## What it does

Evaluates tool invocations against an ordered chain of rule modules (envvars, git, pathsafety, etc.), returning APPROVE, ASK, DENY, or NO-OPINION (serialized as `abstain`, and emitted as `{}` so Claude Code decides). Logs all ASK and DENY decisions plus their outcomes to a SQLite database.

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

### Rule failures (`rule_errors`)

A rule that could not GATHER THE EVIDENCE it needs — a resolver subprocess that timed
out, a `tool_input` it could not parse — reports that separately from its verdict
(`docs/adr/0043-ceta-rule-verdict-vocabulary.md`). The chain continues, so the
decision is unaffected, but the failure is counted per rule and written to
`rule_errors` at PreToolUse.

A row means "this rule failed on this call", so the ABSENCE of rows for a rule is the
evidence it is healthy — no zero-count rows are written. The table exists because the
hook is one short-lived process per tool call: an in-process counter can never show
that a resolver is failing _systematically_.

```bash
# Which rules are failing, and how often (last 7 days)
sqlite3 "$DB" \
  "SELECT rule_name, SUM(error_count) n, MAX(created_at) last_seen,
          MIN(error_sample) sample
     FROM rule_errors
    WHERE created_at >= datetime('now','-7 days')
    GROUP BY rule_name ORDER BY n DESC"
```

`Decision`'s serialized value for "ceta has no opinion" remains `abstain` — the Go
identifier was renamed to `NoOpinion` by that ADR, but the stored string is unchanged,
so every query above keeps working against historical rows.

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

Rules are evaluated in order; the first rule that HANDLES the input wins (Bash compounds
fold most-restrictive-wins). A rule that does not govern the input reports
`hookio.ErrNotApplicable` and the chain continues; a rule that handles it and has no
gate to apply returns NO-OPINION, which is terminal. See
`docs/adr/0043-ceta-rule-verdict-vocabulary.md`.

`internal/setup.RuleChain` is the single source of truth for this list and its order.
Register a new rule **there and nowhere else**: the engine integration suite
(`internal/engine/engine_integration_test.go`) derives its chain from that same
function, so a new rule is automatically exercised by every integration case in its
production band. A rule registered only in a hand-maintained test list is invisible
to the integration suite, which is how `git-directory` shipped non-overridable hard
Rejects with unit coverage only. The list below is documentation and can lag; the code
is authoritative.

1. **config-rules** -- consumer `rules.json` basename allow/block; an `approvedCommands` Approve is argument-blind and absolute for its leaf, so the whole early validator band below is skipped for that leaf (ADR 0040 -- see **approvedCommands** under "Consumer configuration" for the blast radius)
2. **git-directory** -- guards a repository's `.git/` directory by DIRECTION (Bash + file/search tools): a **write** Rejects (the hard block); a **copy-out**, a read whose destination is a write (`cp .git/config /tmp/x`, `cat .git/config > /tmp/x`, `ln -s .git/config /tmp/link`), Asks, because `.git/config` can carry a token in a remote URL; a **plain read** Abstains, so `secrets` and the zone checks below still run and settle it (an ordinary in-project `cat`/`ls`/`stat`/`readlink` of git metadata reaches `allow` via `safe-commands`). Note `secretpath` does not classify any `.git/` path as a secret, so today a plain `.git/config` read reaches `allow` in every spelling whose resolved path sits in a readable zone -- tracked as `pg2-dswtg`
3. **dangerous-commands** -- blanket Reject of inherently dangerous commands (`sudo`, `su`, `doas`, `dd`, `mkfs*`, `fdisk`, `parted`, `mount`, `umount`, `reboot`, `shutdown`, `halt`, `poweroff`, `wget`, `nc`/`ncat`/`netcat`, `telnet`, `sftp`)
4. **secrets** -- prompts (ASK) before any tool touches a well-known credential/secret path (`.credentials`, `auth.json`, `secrets/**`, `.ssh/**`, `.env`, `*token*.json`) so such reads are never silently approved. Each path is tested both as NAMED and symlink-RESOLVED, so a link into a credential directory (`~/mykeys/id_rsa -> ~/.ssh/id_rsa`) is caught too, wherever it resolves to; for Bash the resolving pass covers path-shaped arguments only (a bare word is never absolutized into a file in the cwd)
5. **envvars** -- dangerous environment variables
6. **assume** -- Reject AWS `assume` (assume-role)
7. **webfetch** -- WebFetch to allowed hosts
8. **claudetools** -- AskQuestion, Glob, Grep, BashOutput (read-only approve), etc.
9. **killshell** -- KillShell: approve terminating an agent-owned tracked background shell, else Ask
10. **pathsafety** -- file operations with path-based policies; a WRITE to an agent-config or agent-instruction file that sits directly in a `.claude/` directory (`settings.json`, `settings.local.json`, `mcp.json`, `.mcp.json`, or any `*.md`) Abstains instead of approving, so the verdict stays with Claude Code (ADR 0041). Deeper paths -- `.claude/skills/**`, `.claude/plugins/**`, `.claude/projects/**`, `.claude/plans/**` -- and all READS are unaffected
11. **mcp** -- MCP tool allowlist + read-only-verb approval (search/get/list/read/fetch/check); mutating verbs (create/edit/update/delete/…) abstain
12. **primary-commit** -- Reject a `git commit` on the canonical clone's primary branch in an auto-approving (`bypassPermissions`) session; Abstain otherwise. The verdict rests entirely on WHICH DIRECTORY the commit runs in, assembled from three sources: the session cwd, a `cd` / `pushd` earlier in the same compound command (modelled by the ENGINE, which hands each leaf an already-advanced cwd), and `git -C <path>` on the invocation itself. A directory that does not resolve to a LITERAL path is UNRESOLVED and gets a fail-safe verdict of its own instead of silently falling back to the session cwd: **Ask** interactively, **Reject** in an auto-approving session, and never Approve. The markers are the expansions whose value is a property of run time rather than of the command text -- `$VAR`, `${VAR}`, `$(…)`, backticks, `*` / `?` globs, and a leading `~` -- so `git -C $WT commit` and `cd $WT && git commit` are answered "target unknown" rather than resolved against the session cwd. This matters because the resolver walks UP from the directory it is given, so a mis-resolved worktree path lands on the enclosing CANONICAL clone: an agent working in a nested worktree on a feature branch was hard-denied, and told it was on a primary branch it had never been on (`pg2-h2npt`). A literal RELATIVE `-C` is resolved, not unresolved (it joins onto the cwd deterministically), and the unresolved test is scoped to a `commit` -- `git -C $WT status` is untouched. Both reasons now name the evidence: the primary-branch denial states the directory evaluated and how it was chosen before citing R-6, and the unresolved verdict names the offending token, where it came from, and explicitly denies the primary-branch reading.
13. **git** -- git subcommands. `git tag` → Reject. On `git push`, Reject every spelling of a force-push (`-f`, `--force`, a clustered `-f…`, a `+`-prefixed refspec), of a remote-ref delete (`--delete`, `-d`, a `:ref` refspec), and `--mirror`; also Reject `--force-with-lease` when the refspec is CROSS-BRANCH. Same-branch `--force-with-lease` stays Approve (the post-rebase idiom). Reject a push whose DESTINATION is a NETWORK URL given in place of a remote name (`https://`, `http://`, `git://`, `ssh://`, any other `<scheme>://`, scp-like `user@host:path`, and the same URL via `--repo`), since that sends repository contents to an arbitrary host with no `git remote` change to show for it; a LOCAL destination (`/path`, `./path`, `../path`, `~/path`, `sub/dir`, `file://`) is deliberately NOT gated. `git reset --hard` Abstains in every spelling, so the verdict stays with Claude Code (operator ruling 2026-07-31, `pg2-4yy4r` item 4) -- but a reset run under a redirected `GIT_DIR` / `GIT_WORK_TREE` context still Asks, for every spelling including `--soft`, since the redirect test precedes the `--hard` test. `git clean` Abstains for EVERY spelling, with NO flag inspection (operator ruling 2026-07-30): bare, `-n` / `--dry-run`, `-f`, a clustered `-fdx`, `--force` and its abbreviations all get the SAME verdict, because a flag test is the bug surface here -- `-fdx` is one token, so an exact-token `-f` test would sort the most destructive spelling into a "no force" branch, and every long-flag spelling a matcher misses fails toward Approve. Abstain hands the verdict to Claude Code, so `git clean -fdx` prompts in `default` mode and is auto-approved in an auto-approving mode; that consequence was raised and accepted, and the provably-safe `-n` / `--dry-run` were deliberately NOT carved out to Approve. On `git branch` the verdict is by SAFETY rather than by flag (operator ruling 2026-07-31): Abstain on any spelling from which git's OWN guard has been removed -- the fused `-D` / `-M` / `-C`, and any explicit `-f` / `--force` (including a force CREATION, which silently MOVES an existing ref) -- in any cluster, abbreviation or flag position; Approve every spelling where that guard still stands, which is the read/list forms, plain creation, and `-d` / `-m` / `-c` alone, since git itself refuses the destructive case for each. A `--no-force` / `--no-delete` / `--no-move` / `--no-copy` NEGATION is not its positive form, and a token after `--` is a branch name. `git rebase --interactive` needs an automated `GIT_SEQUENCE_EDITOR`. "Every spelling" INCLUDES git's unique-prefix ABBREVIATIONS: git's parse-options accepts any unambiguous prefix of a long option, so `--har` IS `--hard` and `--interactiv` IS `--interactive`, and each gated long flag is matched by prefix rather than by exact token. `git config` options keep MEASURED abbreviation minimums instead, because a match there shifts the operand count its read/write discrimination rests on. Three independent config-injection routes are also screened, since git can be handed configuration outside a `git config` write: (1) a pre-subcommand `-c <key>=<value>` / `--config-env=...` argv injection (`hasGitConfigInjection`) is a NoOpinion FLOOR -- it demotes an Approve, or replaces a leaf this rule would otherwise not classify, but a decisive Ask/Reject from the subcommand's own verdict still wins; a small allowlist of harmless pairs is exempted, and `--config-env` is always caught unconditionally since its value comes from the environment rather than argv; (2) a `GIT_CONFIG`/`GIT_CONFIG_*` environment assignment visible to the leaf (`GIT_CONFIG_COUNT`/`_KEY_n`/`_VALUE_n`, `GIT_CONFIG_GLOBAL`/`_SYSTEM`/`_PARAMETERS`, etc. -- `hasGitConfigEnvInjection`) demotes an Approve the same way, matched key-blind on the variable name alone -- except the `COUNT`/`KEY_n`/`VALUE_n` triple, which clears when it resolves STATICALLY and every pair it spells out is already in route (1)'s own allowlist (`envConfigTripleCleared`, reusing `clearedConfigFlagPairs`); an unresolvable value, a `COUNT` that disagrees with the keys present, or a mixed set where only some pairs clear all fail closed, and every OTHER `GIT_CONFIG_*` form stays fully key-blind; and (3) a `git config` WRITE to a key in the `gatedConfigKeys` table gets a verdict from that key's MECHANISM class rather than a blanket rule: `configSink` (git later EXECUTES the value or a program it names, e.g. `core.hooksPath`, `core.pager`, `credential.helper`) and `configInterlock` (the value DISABLES a refusal git makes by default, e.g. `clean.requireForce`, `receive.denyCurrentBranch`) both Ask; `configRedirect` (the value REPOINTS a remote at another host, e.g. `remote.<name>.url`, `url.<base>.insteadOf`) Rejects outright, matching `git remote set-url`'s own refusal for the same reason. `git config` reads are unaffected by all three
14. **gh** -- GitHub CLI; `gh pr merge` (immediate) → Reject, `gh pr merge --auto` → Abstain. The PR landing flow is DRAFT FIRST and enforced mechanically rather than by a human seeing a prompt (operator ruling 2026-07-30, `pg2-4yy4r` item 2): `gh pr create --draft` → Approve (the blessed step -- it creates nothing mergeable), `gh pr create` without draft → **Reject** naming the two-step `gh pr create --draft` then `gh pr ready` remedy, `gh pr create --web` → Approve (the browser opens and a human picks draft-or-not, so the CLI creates nothing), `gh pr ready` → **Abstain** and `gh pr ready --undo` → Approve. NO SUBCOMMAND IN THIS RULE MODULE ASKS ANY MORE (operator ruling pg2-psiqh, 2026-08-24): every site that used to Ask -- `gh pr ready`, `gh issue create`, and the `gh api` mutation floor below -- now Abstains instead, an explicit, informed choice to accept auto-approval in an autonomous/headless session or a repo whose settings already allow the underlying Bash call, in exchange for no interactive prompt. Marking ready is still the SINGLE act that makes a PR mergeable (with non-draft creation rejected), and it was ungated entirely (`{}`) before pg2-25oru first gated it at Ask -- so create → `ready` → `gh pr merge --auto` can again run end to end with no person in it in exactly the sessions pg2-psiqh names, which is a known, accepted consequence and not a regression discovered later. Draftness is read in every spelling gh accepts and independently of flag position -- `--draft`, the short `-d`, a clustered `-dw`, `--draft=false`/`=0` (a NON-draft create), pflag's last-one-wins for a repeated flag, and the `new` alias of `create` -- with short clusters truncated at `gh pr create`'s value-taking letters so a `d` inside a VALUE (`-tdocs` sets title "docs") cannot manufacture a false Approve. FLAG ARITY is modeled, so a SEPARATED value -- one passed as its own argv token -- is consumed and never rescanned as a flag: `gh pr create --title -d --body y` and `gh pr create -t -d` title the PR `-d` and create a NON-draft, immediately mergeable PR, so both Reject, while `gh pr create --title x -d` and `gh pr create -t -d --draft` are genuine drafts and Approve. The same walk answers the sibling branches, where the hole was the same shape: `gh pr ready -R --undo` makes the PR READY (the `--undo` is the repo) and so Abstains rather than taking the `--undo` Approve, and `gh pr merge -b --auto` merges NOW (the `--auto` is the merge body) and so Rejects rather than taking the `--auto` Abstain. Each subcommand carries its OWN measured arity table, because the letters collide with opposite meanings (`-m`/`-r` are boolean `--merge`/`--rebase` on `merge` but value-taking `--milestone`/`--reviewer` on `create`); each table enumerates the NO-VALUE flags and treats every other flag as value-taking, so a gh that adds a flag fails toward the stricter verdict instead of rescanning its value. `gh issue create` is unaffected -- its Abstain reads no flag at all. `gh api` is classified by its EFFECTIVE HTTP METHOD, not by the subcommand: a read (GET/HEAD/OPTIONS) → Approve, a mutating method in any spelling (`-X`/`--method`, glued `-XPUT`, `--method=PUT`, and the POST gh defaults to when `-f`/`-F`/`--field`/`--raw-field`/`--input` is present) → Abstain, and a PUT to `/repos/{owner}/{repo}/pulls/{n}/merge` → Reject, mirroring the `gh pr merge` verdict it would otherwise bypass -- the merge Reject, and the draft-aware Approve/Reject split on a raw-API PR create, are Reject/Approve sites and pg2-psiqh leaves both untouched; only the generic mutation floor and the GraphQL-mutation/unreadable-document cases moved off Ask. EVERY verdict above is reached through the same command-path resolution, which skips the flags cobra itself skips when it looks for a command, so a GLOBAL FLAG WRITTEN BEFORE OR INSIDE THE PATH gets the same verdict as the plain spelling: `gh --repo o/r pr create`, `--repo=o/r`, `-R o/r`, glued `-Ro/r`, `gh pr -R o/r merge` (the flag inside the path) and `gh -X PUT api repos/o/r/pulls/5/merge` are all real, accepted gh spellings, and reading the path positionally resolved the resource to the flag instead -- so each of them matched no branch and Abstained via the rule's UNEXAMINED fall-through, bypassing every gh gate at once rather than one of them -- indistinguishable from today's deliberate mutation-floor Abstain by Decision alone now that both are the same value, but distinguished internally by Provenance (an unexamined fall-through is tagged `ProvenanceExhaustion`; a rule's own verdict is not), which is what the gh test suite's global-flag fixtures assert directly rather than trusting Decision equality
15. **monorepo** -- config-driven monorepo command/script boundary (`monorepo` block: `approvedCommands` + `dangerousEnvByWrapper`); Abstains until configured
16. **nix** / **docker** -- nix and docker policies (mount-aware inner eval)
17. **curl** -- config-driven curl approval (`curl` block: `allowedDomainSuffixes` read-only + per-domain `domainMethods`); base generic hosts (localhost/loopback, GitHub read hosts) approved read-only even with no config; only ever Approves or Abstains
18. **ssh** -- config-driven ssh/scp classification (`ssh` block: user allowlist / read-only commands / secret-path / password-auth); Abstains until configured
19. **vault** -- config-driven Vault read/write verb split (`vault` block: `readVerbs` → approve, `writeVerbs` → ask); Abstains until configured
20. **safecmds** -- safe commands with path checks (runs AFTER curl/ssh/vault so a configured command-aware leaf is decided by its dedicated rule). A path argument that is DYNAMICALLY EXPANDED (`$VAR`, `${VAR}`, `$(…)`, backtick) is not statically determinable, so the command is NOT approved -- for READS (`cat`/`head`/`tail`/`less`/`more`/`wc`/`diff`/`sort`/`uniq`/`awk`/`jq`/`tq`/`xxd`/`strings`, plus `sed`/`yq`/`gofmt`/`grep`/`rg` reads, `jar tf|xf`, `bash -n`) exactly as for writes (`rm`/`cp`/`mv`/`mkdir`/`touch`/`chmod`/`tee`). Reads were previously exempt, and one variable hop therefore erased the credential deny-list entirely: `cat ~/.ssh/id_rsa` denied while `F=~/.ssh/id_rsa; cat $F` auto-approved. A credential read is an exfiltration primitive, so the read path warrants the refusal at least as much as the write path. Three exceptions, each deliberate: the **browsing** commands (`ls`/`find`/`du`/`stat`/`file`/`lsof`) expose names, sizes and timestamps but never file CONTENT, so a dynamic path there is not gated; the PROGRAM operand of `awk`/`sed`/`jq` is code whose literal `$` (an awk field reference, a sed end-of-line anchor, a jq `--arg` variable) is not a shell expansion, so it is judged only against the bare-expansion shapes (`awk $F` is still refused, `awk '{print $1}' f` is not); and the refusal is a NON-APPROVAL (the call returns to Claude Code's prompt), not a deny -- restoring a deny would require resolving the variable by intra-command dataflow
21. **kubectl** -- Kubernetes operations (`kubectl` block extensions)
22. **buildtools** -- gradle, pre-commit, bats, etc. (`buildtools` block extensions)
23. **sqlite3** -- sqlite3 read/write/DDL classification

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
    // project-root-relative directory prefixes under which EVERY script is
    // approved regardless of basename or trailing args — for a skill/tool
    // whose helper scripts have unbounded basenames but a fixed directory. A
    // leading/trailing "/" is trimmed and exactly one is re-added before
    // matching, so a prefix can never cross a directory-name boundary
    // (".../scripts" must not match a sibling ".../scripts-evil"). An absolute
    // path under the project root and the equivalent relative path match
    // identically (same normalization the monorepo rule uses). Empty by
    // default: this grants broad, path-shaped trust, so it is opt-in per
    // directory.
    "approvedScriptDirs": [".claude/skills/silver-bullet/scripts"],
    "verbScopedApprovals": [], // [{ "tool": "just", "verb": "lint-rules" }]
    // per-tool flags that CONSUME their value, so the verb-scope matcher does not
    // mistake the value for the verb (`just -f <justfile> <verb>`). `:<n>` declares
    // a multi-token flag (`--set:2`); the glued `-f=<v>` form supplies the first
    // value inline. An UNDECLARED value flag stays fail-safe: its value lands in
    // the verb slot and the command Abstains — so leave execution-altering flags
    // (`--shell`, `-c/--command`, `--dotenv-path`) out on purpose.
    "valueFlags": { "just": ["-f", "--justfile", "-d", "--working-directory"] },
    // per-tool flags KNOWN not to alter execution. The PRESENCE of a tool key —
    // even with an empty list — makes that tool STRICT: a dash token is skipped
    // only if it is a declared value flag or a declared allowed flag spelled BARE,
    // and anything else resolves NO verb, so the command Abstains. That is what
    // closes the glued form (`just --shell=/bin/x check`), a bare dangerous flag
    // (`--no-dotenv`), `--`, a clustered short group (`-nq`) and an attached short
    // value (`-E/tmp/x`) — none of which a deny list can cover. A tool with no key
    // here is unchanged. Fails CLOSED: an unlisted flag costs a prompt, never an
    // approval.
    "allowedFlags": { "just": ["--quiet", "--verbose", "--dry-run"] },
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
  from secret-path detection and from
  `git-directory`'s hard deny, the latter of which is otherwise **not**
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
  regress: the engine's redirection check (`grawrap > /etc/hosts` → Reject), a
  sibling leaf judged on its own (`grawrap && sudo rm -rf /` → Reject), and
  `config-rules`' own withhold when the leaf carries inline env assignments
  (`FOO=bar grawrap build` → Abstain). See
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

## Testing

```bash
go test ./...                     # unit tests — what a package BUILD runs
go test -tags integration ./...   # the above PLUS the binary-exec integration suite
```

### Two suites, and why the split exists

The tests in `cmd/claude-extended-tool-approver` that **exec the compiled binary** —
every `runHook` / `runCLI` caller, and everything that drives a real ask log through it —
live in `*_integration_test.go` files behind the `integration` build tag, so a default
`go test ./...` never sees them. This mirrors the repo's existing tag idiom
(`pa-monitor`'s `hostile`, `pb`'s `contract` / `smoke`), sanctioned by ADR 0021.

The split is not cosmetic. `mkGoApp` scopes gomod2nix's check hook to `subPackages`, so
the **only** tests a package or `nixosConfiguration` build runs are this one package's —
not the ~1,020 `internal/*` unit tests. Before the tag, that meant ~46 binary-exec tests
were on the deploy path. On 2026-08-16 they took **559.33s with zero failures** and the
51st was still running when Go's 10m alarm fired, taking the whole monorepod build down
with it. The wall clock was `(fsync count) x (an unbounded host property)`, so no
`-timeout` can be chosen that a slower disk cannot blow through — the fix is to take them
off the build path, not to budget for them.

Where each kind belongs:

| Adding a test that…                          | Goes in                            | Runs during a build? |
| -------------------------------------------- | ---------------------------------- | -------------------- |
| calls production functions in-process        | `cmd_evaluate_test.go` (untagged)  | yes                  |
| execs the binary, or calls `asklog.NewStore` | a `*_integration_test.go` (tagged) | **no**               |

CI still runs the tagged suite, as the `claude-extended-tool-approver-integration-tests`
flake check (`mkGoTest` with `testFlags = ["-tags" "integration"]`). That check is reached
by `nix flake check` and never by a package build, so a degraded disk can slow CI but can
no longer block an activation.

### Why it is I/O-bound

The suite is **I/O-bound, not CPU-bound**, because most of it exercises the SQLite ask
log. Every test builds a throwaway database under `t.TempDir()`, and creating one costs
roughly 11 `fsync` calls — 17 before `migrate` was made atomic — (the `journal_mode=WAL`
conversion, the schema-migration commit, and the close checkpoint).

`fsync` latency is a property of the **host filesystem** and varies by orders of
magnitude — measured on the Linux dev host for this repo, ~50ms per `fsync` on the ext4
root that backs `/tmp` versus ~0.8us on tmpfs, a ~60,000x spread. On the slow end that
is enough to dominate wall-clock time entirely.

Two consequences worth knowing before you tune a timeout:

- `internal/asklog` sets `PRAGMA synchronous=OFF` for its own tests via `TestMain`
  (see `synchronousPragma` in `internal/asklog/store.go`). Durability is meaningless for
  a database deleted at test exit, and without this the 73-test package took **2m10s of
  wall clock for 0.9s of CPU** on a slow-`fsync` host; with it, **1.2s**. Production is
  unaffected and still runs at SQLite's default `synchronous=FULL`.
- `cmd/claude-extended-tool-approver`'s **integration** tests exec the **real binary** as
  a subprocess, so they deliberately run production code with full durability and cannot
  use the pragma seam. Its `TestMain` sets the pragma for the in-process helpers only;
  the exec'd binary keeps shipped durability, because no env var or flag may change how
  the real binary behaves (`16e1fd4d`, `pg2-iay90`).
- `TestMain` also points `TMPDIR` at `/dev/shm` when one is present and writable, so the
  scratch tree — both the in-process stores and the child's `XDG_DATA_HOME`, since
  `t.TempDir()` and `os.MkdirTemp("")` both resolve through it — lands in RAM. That makes
  the residual flushes cheap **without changing a single shipped semantic**. It is a
  preference, never a requirement: absent on macOS, and nix build sandboxes mount their
  own (`sandbox-dev-shm-size`, 50% of RAM by default). When it is missing the suite is
  merely slower, never broken.

If this suite ever looks like it is hanging, check wall-clock against CPU time first. A
`go test` timeout panic whose stack sits inside `modernc.org/sqlite`'s pager open is the
signature of slow `fsync`, **not** a deadlock — the timeout simply lands on whichever
test happened to be running. The give-away is the shape of the run: pure-CPU tests still
report `0.00s` while anything touching disk reports tens of seconds.

## Dependencies

Go deps are not vendored. The Nix build uses the **gomod2nix** engine: `default.nix` passes
`gomod2nixToml = ./gomod2nix.toml`, and that file is committed beside `go.mod`. There is **no
`vendorHash`** for this package — do not reintroduce one. After changing Go dependencies (adding or
removing an import, `go get`, `go get -u`), regenerate it:

```bash
go mod tidy && nix run github:nix-community/gomod2nix -- generate   # then COMMIT gomod2nix.toml
```

`./update-deps.sh` is the wrapper for the same thing (it delegates to
`../update-gomod2nix-deps.sh`), and the workspace-level `../../update-locks.sh` refreshes
everything at once. Authority: `phillipg-nix-repo-base` ADR 0008 and this repo's `CLAUDE.md`
under "Versioning of Custom Packages". A missing regeneration fails the Nix build, which
`nix flake check` catches.

## Command structure: the `cmdparse` parser seam

`internal/cmdparse` is migrating from several independent text passes to ONE real shell parser
(`mvdan.cc/sh/v3`) behind a single lowering seam, per
[ADR 0039](../../docs/adr/0039-ceta-shell-parser-front-end.md).

- `internal/cmdparse/shellparse.go` is the **seam** — the only file in this module allowed to import
  `mvdan.cc/sh/v3`, enforced by `TestSeamIsTheOnlyParserImporter`.
- It currently runs in **SHADOW**: the outgoing front end (`StripCommentsPreservingHeredocs` then
  `Parse`) is still authoritative for every verdict, and disagreements are logged to stderr. Set
  `CETA_SHADOW_PARSER=0` to skip the second parse.
- `internal/cmdparse/LOWERING.md` records the per-construct coverage, the corpus population later
  migration steps must cite, and the latency-gate result.
