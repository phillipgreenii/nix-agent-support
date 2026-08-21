---
name: pull-request
description: Push-and-open/update-PR landing handler (PR-0..PR-4). Invoked by the `integrate-branch` skill as its `pull-request` handler when the resolved strategy is "pull-request" — not normally invoked directly. Never auto-merges; keeps the branch and worktree.
---

# pull-request handler

This is the **Command-style handler** for the `pull-request` integration strategy:
push the current branch to its remote, open a new pull request or update an
existing one, then stop — landing the PR is a separate, explicit human action that
this handler never performs. It is invoked by the `integrate-branch` skill (via the
`Skill` tool, using the strategy string as the skill name) — do not invoke it
directly unless you are deliberately replaying its flow outside the dispatcher.

Because that dispatch goes through the `Skill` tool, this handler MUST NOT set
`disable-model-invocation` in its frontmatter. The flag is enforced against the
`Skill` tool and also drops the entry from the model-visible skill listing, so
setting it breaks both halves of the dispatcher's Step 4 — its "is this strategy
installed" check and the dispatch itself. It was set here once as a listing-token
saving and reverted for exactly this reason (bd `pg2-okzl0`); the prose above is
the only sanctioned deterrent against invoking a handler directly.

Skills receive no typed arguments, so this handler **re-derives its own context
from git** rather than trusting values handed to it — the same discipline
`ff-merge-to-main` follows. It also re-checks the canonical clone independently
rather than trusting the caller's anomaly report, even though (unlike
`ff-merge-to-main`) that anomaly never blocks this handler.

## Step 0 — Re-derive context from git

Do not assume `<WT>`, `<FB>`, `<CC>`, or the primary branch were passed in —
compute them fresh, identically to `ff-merge-to-main`'s Step 0 (so the two
handlers always agree):

```bash
WT="$(pwd)"                                   # the current working tree
FB="$(git rev-parse --abbrev-ref HEAD)"        # the feature branch
CC="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"   # canonical clone (main worktree)
# primary branch — same resolution integrate-branch-support uses (declared → origin/HEAD → main)
PRIMARY="$(git config --get pgii-integrate-branch.primaryBranch \
  || git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null \
  || echo origin/main)"
PRIMARY="${PRIMARY#origin/}"                    # strip the remote prefix if it came from origin/HEAD
```

- **`<WT>`** = the current working tree — wherever this handler is running.
- **`<FB>`** = the current branch. If `git rev-parse --abbrev-ref HEAD` prints
  `HEAD` (detached), there is no feature branch to integrate — **halt and report**
  "nothing to integrate: detached HEAD," and stop here.
- **`<CC>`** = the canonical clone, i.e. the **main working tree** of the common
  git dir (`git rev-parse --git-common-dir` resolved to its parent directory).
  Unlike `ff-merge-to-main`, this handler never writes to `<CC>` — it only reads it
  to surface an anomaly (PR-0a below).
- **primary branch** = the shared resolution (same one `integrate-branch-support`
  and `ff-merge-to-main` use): `git config --get
pgii-integrate-branch.primaryBranch` → else `git symbolic-ref
refs/remotes/origin/HEAD` (strip the `refs/remotes/origin/` prefix) → else `main`.
  It is the PR's base branch.

## PR-0 — Precondition: canonical anomaly surfaced, and `<WT>` actually pushable

Like `ff-merge-to-main`'s FF-0, this checks **two** trees — but for a different
reason than FF-0's. FF-0a/FF-0b split because FF-1 and FF-2 depend on different
trees (`<WT>` and `<CC>` respectively); here `<CC>` is never written at all, so
PR-0a stays a **surface-only** report and PR-0b is the one **blocking**
precondition, guarding the single tree this handler actually acts on.

### PR-0a — Surface a canonical anomaly (non-blocking)

Re-check the canonical clone independently — do not trust the caller's report:

```bash
git -C "$CC" rev-parse --abbrev-ref HEAD   # compare to $PRIMARY
git -C "$CC" status --porcelain            # non-empty means dirty
```

If `<CC>` is off `<PRIMARY>` or dirty, this is a Tier R **R-3 anomaly** and MUST be
surfaced to the user. Unlike `ff-merge-to-main`'s FF-0a, this is **not** a halt
condition here: the `pull-request` method never reads from or writes to `<CC>` — it
only pushes `<WT>`'s branch to a remote and talks to a PR host — so an off-primary
or dirty canonical clone has nothing this handler could damage. Report the note and
continue to PR-0b regardless.

### PR-0b — the worktree PR-1 will push (`<WT>`)

Unlike PR-0a, this check **blocks** — mirroring `ff-merge-to-main`'s FF-0b
exactly, because the failure mode is the same one FF-0b exists to prevent: a
dirty `<WT>` pushed as-is silently **omits** the uncommitted work, so the branch
that lands in the PR is not the branch the author actually has. The author sees
a PR that looks finished and is not.

```bash
git -C "$WT" status --porcelain            # MUST be empty
```

If `<WT>` is dirty, **halt and report** `stopped:worktree-dirty` with the absolute
path of `<WT>` and the `status --porcelain` output — reusing FF-0b's exact reason
vocabulary rather than inventing a parallel one, since the disposition is
identical: the operator commits or stashes in `<WT>`, then re-invokes
`integrate-branch`. This is a **caller-state** halt, not a Tier R anomaly (the
same distinction FF-0b draws) — the handler MUST NOT commit or stash on the
caller's behalf; that silently decides the fate of work it did not create.

`pull-request` has no rebase step, so FF-0b's second check (a rebase already in
progress) has no equivalent here — there is nothing in this flow a leftover
rebase state could corrupt, so only the dirty-worktree half of FF-0b applies.

## PR-1 — Push the feature branch to its remote

Resolve which remote to push to the same way `integrate-branch-support` resolves
remote ambiguity: prefer the branch's existing upstream; fall back to the sole
remote if there's exactly one; anything else is ambiguous.

```bash
REMOTE="$(git -C "$WT" rev-parse --abbrev-ref --symbolic-full-name "@{u}" 2>/dev/null | cut -d/ -f1)"
if [ -z "$REMOTE" ]; then
  n="$(git -C "$WT" remote | grep -c .)"
  # 2+ candidate remotes and no upstream set → AMBIGUOUS: stop and report
  # `stopped:ambiguous-remote` (do NOT fall through to the push and guess a remote).
  [ "$n" -gt 1 ] && exit 1
  REMOTE="$(git -C "$WT" remote)"   # the sole remote when n==1 (empty if none)
fi
```

If no remote can be resolved unambiguously (no upstream, and more than one
candidate remote), **halt and report** `stopped:ambiguous-remote` rather than
guess. This should be rare in practice — `integrate-branch`'s feasibility check
(Step 4) already confirmed a `remote` exists before invoking this handler — but the
handler re-verifies rather than trusting that.

### Pushing, and classifying a push failure

```bash
git -C "$WT" push -u "$REMOTE" "$FB"
```

can fail for several **different** reasons that need different dispositions, so
classify the failure rather than treating every non-zero exit the same way.
`attempts = 0`.

- **Exit 0 — push succeeded.** If `<FB>` already had commits pushed and an open
  PR exists, this push is what "updates" that PR — most PR hosts pick up new
  commits on the branch automatically; no separate "update" API call is needed
  for the commits themselves. Proceed to PR-2.
- **Non-fast-forward rejection** — git names it explicitly on the ref line
  (`[rejected]` with `non-fast-forward` or `fetch first`): a peer has advanced
  `<REMOTE>/<FB>` since this handler last read it — exactly the case
  `/drain-beads` calls **TRANSIENT** for a shared `drain/<id>` branch. Handle it
  as a **bounded retry**, in the same shape as `ff-merge-to-main`'s FF-3:
  - `attempts++`, then re-fetch and **rebase `<WT>` onto the updated remote
    branch**, and re-attempt the push:
    ```bash
    git -C "$WT" fetch "$REMOTE" "$FB"
    git -C "$WT" rebase "$REMOTE/$FB"
    git -C "$WT" push --force-with-lease -u "$REMOTE" "$FB"
    ```
    `--force-with-lease` **IS permitted** for this retry — it only overwrites the
    remote ref if it still matches what this rebase just fetched, so it cannot
    silently clobber a commit this retry never saw. Bare `--force` is
    **FORBIDDEN** here and everywhere else in this handler.
  - If that rebase itself conflicts, apply the same discipline as
    `ff-merge-to-main`'s FF-1: resolve it confidently and continue (summarizing
    the resolution), or abort and **halt and report** `stopped:rebase-conflict` —
    reusing that existing reason rather than inventing a parallel one, since it
    is the same state. Do not let an unconfident conflict resolution turn into a
    forced push.
  - When `attempts` reaches **2** (the second consecutive non-fast-forward
    rejection), **stop and ask** the user rather than retry indefinitely — a
    persistent push race warrants attention, matching FF-3's framing (R-7).
    **Halt and report** `stopped:push-non-fast-forward` with `<REMOTE>/<FB>` and
    git's rejection message.
- **Auth failure** — git names it explicitly (`Authentication failed`,
  `Permission denied`, `could not read Username`, or the transport refusing
  outright, e.g. `fatal: Could not read from remote repository`). Retrying will
  not help — the credentials or access, not the ref state, are the problem:
  **halt and report** `stopped:push-auth-failed` with git's message verbatim. The
  operator fixes credentials/access, then re-invokes `integrate-branch`.
- **Anything else (including a pre-receive/policy hook rejection)** — **halt and
  report** `stopped:push-failed`. This is the catch-all, and it MUST assert **no
  specific cause**: relay git's message verbatim and stop there — do **not**
  guess at or name a plausible-sounding cause the handler did not actually
  observe. A prior bug, `pg2-k3s0x`, was caused by exactly a catch-all that
  invented one; the fix is that this outcome is reported as an **unspecified
  push failure**, nothing more. The operator reads git's own message to
  diagnose it, then re-invokes `integrate-branch`.

## PR-2 — Open a new PR, or confirm the existing one

Detect whichever PR host tool is available in this repo, and use it to check for an
existing **open** PR whose head is `<FB>`, rather than trusting a caller-supplied
`open_pr` value (it may be stale by the time this handler runs):

- **`gh` (GitHub CLI) present:**

  ```bash
  gh pr view "$FB" --json url,state,number
  ```

  A found PR in `state: "OPEN"` means PR-1's push already updated it — report
  `pr-updated` with its `url`. Nothing found (or a non-open state, e.g. it was
  closed/merged and this is fresh work) → open one:

  ```bash
  gh pr create --draft --head "$FB" --base "$PRIMARY" --fill
  ```

  and report `pr-opened` with the printed URL.

  `--draft` is **REQUIRED**, not a preference: this workspace lands draft first, and
  the `claude-extended-tool-approver` PreToolUse hook **hard-rejects** a `gh pr create`
  without it (operator ruling 2026-07-30) — a Reject is not overridable in-session, so
  a non-draft spelling does not prompt, it FAILS with a message naming this two-step.
  Promoting the PR to ready is the second step and a **human** one (`gh pr ready`,
  which prompts); this handler MUST NOT run it, for the same reason PR-3 forbids the
  merge verbs.

- **`pg-pr` present (no `gh`):** consult its own listing (e.g. `pg-pr pr list
--json`, filtered to a head of `<FB>`) for an existing open PR. Found → report
  `pr-updated`. Not found → `pg-pr pr create --head "$FB" --base "$PRIMARY" --title
"<derived title>"` (a title is required; derive it from the branch's commit
  history — e.g. the first commit's subject — or ask the user if the branch mixes
  unrelated work) and report `pr-opened` with the returned URL.
- **Another configured forge CLI:** the same pattern — probe for an existing open
  PR on `<FB>` first; only create one if none is found; use whatever base-branch and
  title flags that CLI exposes, targeting `<PRIMARY>`.
- **None of the above installed:** **halt and report** `stopped:no-pr-host` — a
  remote existing does not mean there's a way to open a PR against it from here.

## PR-3 — Never auto-merge (hard rule)

This handler's job **ends** at opening or updating the PR. Merging a PR-driven
repo requires **explicit human permission** in this workspace — it is not this
handler's decision to make, no matter how confident the checks look or how the
request is framed (e.g. "just land it," "merge if green"). Concretely, this
handler MUST NOT run, at any point, for any reason:

- `gh pr merge` (with or without `--auto`)
- `pg-pr pr merge` or `pg-pr pr automerge on` — both of which `pg-pr` itself
  labels human-only verbs (its own CLI prints `WARNING: merge is a human-only
verb` / `WARNING: automerge is a human-only verb` if invoked)
- any other forge CLI's merge or enable-automerge equivalent

If the user wants the PR actually merged, that is a separate action outside
`integrate-branch` entirely — they merge it themselves via the PR host's UI or CLI.
This handler reports the PR as opened/updated and stops there.

## PR-4 — Keep the branch and worktree

Unlike `ff-merge-to-main`'s FF-4, this handler performs **no cleanup**. The work is
not yet integrated — the PR is open, not merged — so `<WT>` and `<FB>` MUST remain
exactly as they are. Do not run `git worktree remove`, `git branch -d`, or any
equivalent. The worktree and branch are retired later, by whichever handler
eventually lands the merged PR (or manually, once the human has merged it).

## Decision flow

```mermaid
flowchart TD
    A["agent in WT on FB"] --> P0A["PR-0a: check CC vs PRIMARY (surface only, never halts)"]
    P0A --> P0B{"PR-0b: WT clean?"}
    P0B -->|"dirty"| S7["STOP: stopped:worktree-dirty — operator commits or stashes in WT"]
    P0B -->|Yes| D{"FB detached?"}
    D -->|Yes| S0[STOP: nothing to integrate]
    D -->|No| R{"remote resolvable?"}
    R -->|No / ambiguous| S1[STOP: stopped:ambiguous-remote]
    R -->|Yes| PUSH["PR-1: git push -u REMOTE FB"]
    PUSH --> PRES{"push result?"}
    PRES -->|"exit 0"| HOST{"PR host tool available?"}
    PRES -->|"non-fast-forward"| RETRY{"attempts < 2?"}
    RETRY -->|Yes| REB["fetch + rebase WT onto REMOTE/FB,\npush --force-with-lease"] --> PUSH
    RETRY -->|No| S8["STOP: stopped:push-non-fast-forward"]
    PRES -->|"auth failure"| S9["STOP: stopped:push-auth-failed"]
    PRES -->|"anything else"| S10["STOP: stopped:push-failed — no cause asserted"]
    HOST -->|None| S2[STOP: stopped:no-pr-host]
    HOST -->|gh / pg-pr / forge CLI| PROBE{"open PR for FB exists?"}
    PROBE -->|Yes| UPD["report pr-updated"]
    PROBE -->|No| CREATE["create PR (base = PRIMARY)"] --> OPEN["report pr-opened"]
    UPD --> KEEP["PR-4: keep WT and FB — no cleanup"]
    OPEN --> KEEP
```

## Reporting the outcome

Report the result back using the shared handler vocabulary: `pr-opened` (a new PR
was created), `pr-updated` (an existing open PR already covers `<FB>`, and PR-1's
push refreshed it), or `stopped:<reason>` — detached HEAD, a dirty `<WT>` at PR-0b
(`worktree-dirty`), an unresolvable remote (`ambiguous-remote`), no PR host tool
available (`no-pr-host`), or one of PR-1's push-failure outcomes
(`push-non-fast-forward` after the retry cap, `push-auth-failed`, or the
no-cause-asserted catch-all `push-failed`). Always include the PR's URL when one
exists. This handler never returns `landed` — that outcome belongs to
`ff-merge-to-main`, and this handler never merges anything.

## Rules this handler enforces (Tier R, RFC 2119)

- The handler MUST re-derive `<WT>`, `<FB>`, `<CC>`, and the primary branch from
  git itself rather than trusting caller-supplied values (skills have no typed
  arguments).
- On a detached `HEAD` in `<WT>`, the handler MUST halt and report "nothing to
  integrate" rather than guess at a feature branch.
- The handler MUST re-check `<CC>`'s branch and cleanliness itself (PR-0a) rather
  than trusting a caller-supplied anomaly report, and MUST surface any anomaly
  found (R-3) — but MUST NOT halt on it, because the `pull-request` method never
  reads from or writes to the canonical clone (R-8's carve-out for non-canonical-
  advancing methods).
- The handler MUST check `<WT>` itself is clean (PR-0b) **before** PR-1 pushes it,
  and MUST halt and report `stopped:worktree-dirty` — the same reason
  `ff-merge-to-main`'s FF-0b uses — if it is not. It MUST NOT commit or stash on
  the caller's behalf; that disposition belongs to the operator.
- The handler MUST resolve the push remote itself and MUST halt rather than guess
  when it cannot resolve one unambiguously.
- The handler MUST classify a push failure rather than treating every non-zero
  `git push` exit the same way: a non-fast-forward rejection, an auth failure,
  and any other failure each get a **distinct** `stopped:<reason>`.
- On a non-fast-forward rejection the handler MAY retry by rebasing `<WT>` onto
  the updated `<REMOTE>/<FB>` and re-pushing with `--force-with-lease`, but MUST
  NOT use bare `--force` under any circumstance. It MUST bound this retry and
  stop-and-ask after the **second** consecutive rejection (matching
  `ff-merge-to-main`'s FF-3 cap), reporting `stopped:push-non-fast-forward`.
- On an auth failure the handler MUST halt immediately and report
  `stopped:push-auth-failed` rather than retry — retrying cannot fix a
  credentials/access problem.
- On any push failure that is neither of the above, the handler MUST report the
  catch-all `stopped:push-failed` and MUST NOT assert a specific cause it did not
  actually observe — relay git's message verbatim rather than guess (the
  `pg2-k3s0x` lesson).
- The handler MUST check for an existing open PR on `<FB>` before creating a new
  one, so re-running this handler updates rather than duplicates a PR.
- The handler MUST NOT merge the PR, enable PR automerge, or invoke any
  human-only merge verb (`gh pr merge`, `pg-pr pr merge`, `pg-pr pr automerge on`,
  or a forge-CLI equivalent) under any circumstance — merging a PR-driven repo in
  this workspace requires explicit human action outside this handler.
- The handler MUST NOT remove, retire, or otherwise clean up `<WT>` or `<FB>` —
  unlike `ff-merge-to-main`'s FF-4, cleanup does not belong to this handler because
  the work is not yet integrated.
- The handler MUST report the PR's URL and one of `pr-opened` / `pr-updated` /
  `stopped:<reason>` — never `landed`.
