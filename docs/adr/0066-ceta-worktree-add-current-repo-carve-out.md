# CETA `git worktree add` carve-out: current-repo-root, not temp-root

**Status**: Accepted (resolves `tc-uelj`)
**Date**: 2026-09-11
**Deciders**: Phillip Green II

## Context

`docs/adr/0059-ceta-temp-repo-carve-out.md` and its follow-on `tc-mzr5` extend CETA's `gitdir`
rule to Reject a PERSISTENT redirect of a repository's worktree/git-directory location — `git
config core.worktree`, `git config --file <path> … core.worktree`, `git init
--separate-git-dir`, and `git worktree add`/`git worktree move` — unless every repo-locating
operand the command carries resolves under a temporary root (`internal/temproot`). That refusal
is right for the actual hazard tc-mzr5/tc-7mqr are about: an undetermined writer set
`core.worktree` in this repo's own canonical clone's `.git/config` to a workforest path, so every
subsequent `git` invocation against the canonical clone silently operated on the workforest's
files instead.

It also, unconditionally, blocked a normal and expected agent operation. While working `tc-bqyp`
(2026-09-10), an agent in the homelab canonical clone attempted:

```
git worktree add .worktrees/tc-bqyp -b drain/tc-bqyp
```

— exactly the pattern this repo's own `CLAUDE.md`/agent rules R-4 MANDATE by default ("isolated
single-repo change MUST be done in a git worktree") and this repo's own convention already uses
extensively (`.worktrees/*`, `homelab-worktrees/*`). CETA rejected it outright, because the
target does not resolve under a temporary root — the same treatment as an actual redirect hazard,
even though creating an ADDITIONAL, independent worktree of the repo the agent is already in is a
materially different act from redirecting an EXISTING checkout elsewhere.

Operator position (Phillip, 2026-09-10, verbatim): "you should always be allowed to do that."

## Decision

**`git worktree add <path> [-b <branch>]` is additionally approved when `<path>` resolves under
the CURRENT repository's own root** — the git repository enclosing the invocation's own effective
directory, found by the same raw `.git`-ancestor walk `internal/patheval`'s `InGitRepo`/
`DetectProjectRoot` already use internally, now exported as `patheval.GitRoot` for this purpose.
This is a SECOND, independent carve-out alongside the pg2-yoqsr/tc-mzr5 temp-root one — CETA's
`gitdir` rule relaxes its Reject when EITHER carve-out applies — keyed on a different root
(the enclosing repository, not a machine-wide temp-root set) and scoped to a different act
(creating an additional worktree, not building a disposable fixture).

### Only `add` is eligible

`git worktree move`, `git config core.worktree` (any scope spelling, or `--file <path>`), and
`git init --separate-git-dir` remain Rejected exactly as before this decision, REGARDLESS of
whether their own target happens to resolve under the current repo's root. Every one of those
three redirects something that ALREADY EXISTS — the tc-mzr5/tc-7mqr hazard — while `add` creates a
NEW, independent worktree. `git worktree remove`/`list`/`prune`/`repair` were already unaffected
before this decision (tc-mzr5's own `gitWorktreeSubcommandTarget` never matches them — see its
doc — so `gitdir`'s Reject never fires for them, and `internal/rules/git`'s own
`TestGit_Worktree_Approve` already approves them at that rule's scope); this decision does not
change that.

GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE/GIT_COMMON_DIR/GIT_OBJECT_DIRECTORY and
`--git-dir`/`--work-tree` are untouched by this decision — that refusal is `tc-j0aa`'s separate,
explicitly out-of-scope question, same as it was for tc-mzr5.

### The root is the raw `.git` walk, not `DetectProjectRoot`'s monorepo-aware answer

`patheval.DetectProjectRoot` additionally honours `MONOREPO_ROOT` when the cwd lies under it,
which would admit an unrelated sibling repo's directory as a valid worktree-add target — a
broader grant than "the repo you are already in." This decision uses the raw walk
(`patheval.GitRoot`, the same primitive `InGitRepo` is built on) instead, so the carve-out
approves creating a worktree of the SPECIFIC repository enclosing the invocation, and nothing
broader.

### Fail-safe on no repo found, and on any other hazard in the same compound

- A fabricated, not-yet-existing, or not-a-repo cwd never relaxes anything: `patheval.GitRoot`'s
  `found=false` case is fail-safe, matching `tempFixtureCarveOutApplies`' own "no participants,
  never relax" default.
- A compound command carrying ANY OTHER `.git`-metadata hazard alongside an eligible `worktree
add` — a plain `.git/config` write on a different leaf, or a `GIT_DIR=<real canonical>/.git git
status` leaf riding alongside it — disqualifies the WHOLE command. `git worktree add
.worktrees/x -b y && rm -rf .git` MUST NOT be relaxed merely because the first half qualifies.
  This mirrors pg2-yoqsr's own "every participant must independently qualify" rule, restated for
  this carve-out's own participant set (see `internal/rules/gitdir`'s
  `currentRepoWorktreeAddCarveOutApplies` for the exact walk).

## Consequences

- `git worktree add <path-under-the-current-repo-root> [-b <branch>]` — this repo's own R-4
  everyday operation — is approved from a real (non-temp) checkout, closing the gap `tc-bqyp`
  surfaced.
- `git worktree move`, `git config core.worktree`, `git init --separate-git-dir`, and the
  GIT_DIR/GIT_WORK_TREE family stay refused exactly as before this decision, including when their
  own target happens to resolve under the current repo's root — only `add` gained a carve-out.
- `git worktree add` to a target OUTSIDE the current repo's root (an unrelated absolute path, or
  a path that escapes the repo via `../`) stays refused exactly as before this decision — the
  carve-out is scoped to the specific repository enclosing the invocation, not "any `worktree
add` whatsoever."
- The reason text for a Rejected `worktree`/git-dir redirect now names this SECOND carve-out
  alongside the pg2-yoqsr temp-root one, so an agent that hits the refusal can tell which relief
  applies to the command it tried.
