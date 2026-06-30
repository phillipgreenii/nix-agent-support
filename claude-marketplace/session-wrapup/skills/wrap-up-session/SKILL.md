---
name: wrap-up-session
description: >-
  Use when the user wants to close out a coding session — "wrap up this session", "session
  wrapup", "wrap up the session", "land the plane", "let's call it / call it here", "finish up
  before I go", "clean up and stop". Autonomously lands the work this session touched: commits
  outstanding changes, closes completed beads and files beads for discovered + unfinished work,
  runs the repo's test/lint/build gates, integrates each touched repo (local ff-merge to main,
  or push branch + open/update PR for PR-based repos), removes spent branches and worktrees,
  syncs beads to the remote, and — if any work carries over, including work deferred to next
  session — writes a single P0 next-session bead so the next session can resume cold. pn-workspace aware; acts only on what THIS session worked on and
  leaves everything else untouched. Do NOT use for mid-session commits, for grooming the
  backlog (that's bead-grooming), or for merging someone else's PR.
---

# Wrap up this session

End-of-session ritual that takes a working session from "I'm done for now" to a clean,
durable state: nothing uncommitted, nothing un-tracked, finished work integrated, spent
branches gone, and — if anything's left — a single P0 bead holding the prompt to resume.

This is the thing you'd otherwise re-type by hand every time you stop. It runs
**autonomously**: gather state, do the whole sequence, report what happened. You don't pause
for per-step approval. Two things keep autonomy safe, and they are the heart of this skill:

1. **Strict session scope.** You touch only the repos, branches, worktrees, and beads that
   _this session_ worked on. Anything else — a dirty file you didn't create, a branch from
   another effort, a stash that predates the session — you leave exactly as you found it and
   note it in the summary. When you can't tell whether something belongs to this session,
   treat it as out of scope. The cost of skipping is a line in the report; the cost of
   guessing wrong is clobbering unrelated work.

2. **Gate before you integrate.** Merging or pushing broken work is the one mistake that's
   expensive to walk back. So quality gates run _before_ any irreversible step, and a failure
   stops integration cold (the branch stays, a bead gets filed, the next-session prompt
   explains it) rather than landing red code on main.

## Why "scope to the session" is the whole game

A wrapup that operates on "the repo" is easy and wrong. Real workspaces are messy: a
half-finished experiment on another branch, a teammate's worktree, a stash you forgot. The
value of this skill is that the user can say "wrap up" and trust that _their session's_ work
lands and _nothing else moves_. So before doing anything destructive, build an explicit
picture of what this session is, and let that set drive every later step.

Signals that something is in-session (gather these first, read-only):

- **The branch/worktree you're in.** The cwd's repo, its current branch, and whether it's a
  git worktree or a `pn` coordinated workforest set. This is almost always the spine of the
  session.
- **Beads you moved.** `bd list --status in_progress` and anything you `--claim`ed or created
  this conversation. These name the work's intent.
- **Dirty + ahead state.** `git status` (uncommitted changes) and `git log @{u}..` /
  `git log main..` (commits not yet on main) in the repos you've been editing.
- **The conversation itself.** What files did you edit, what did the user ask for? You have
  this context — use it. If you implemented feature X on branch `feat-x`, that branch is
  in scope; the unrelated `fix-y` branch is not.

If after this you're still unsure whether a given branch/repo/change belongs to the session,
**exclude it** and say so.

## Reading the terrain

The same ritual has to work in three shapes. Detect which you're in, don't assume:

- **Standalone repo** — cwd is (or is under) a single git repo, no `pn-workspace.toml`
  upward. Operate on that one repo.
- **pn workspace** — a `pn-workspace.toml` exists at/above cwd (or `PN_WORKSPACE_ROOT` is
  set). Multiple repos share a root and may share coordinated workforest _sets_. Scope still
  applies: act only on the repos this session changed, but be aware that a `pn` workforest set
  spans repos and is removed as a unit. See `references/integration-and-cleanup.md`.
- **PR-based repo** — a repo where finished work lands via pull request, not a local merge.
  Detected per repo (below), not assumed globally — in this workspace `homelab` and the
  `nix-*` repos can differ.

Per-repo integration flow is **detected, not hardcoded**. For each in-scope repo on a feature
branch:

- An **open PR** for the branch (`gh pr view` / forgejo / `pg-pr pr show`) **or** a `pg-pr`
  merge-request tracker bead (`bd list --type=merge-request`) → **PR flow**: push the branch,
  open or update the PR, do **not** merge it.
- **Neither** → **local ff-merge flow**: rebase onto `main`, fast-forward merge into `main`,
  then retire the branch + worktree. **No push** — landing on local `main` _is_ the
  integration; publishing `main` to a remote is not part of wrapup for a merge-to-main repo.
- Work done **directly on `main`** → just commit; there's nothing to merge, push, or retire.

Pushing is a **PR-flow-only** action: you push a branch so it can be reviewed. A merge-to-main
repo never pushes during wrapup — that's the single most important thing to get right here, and
it's why this skill won't trip a "don't push to the default branch" guard.

Why PRs are never auto-merged: a PR is a request for _someone else's_ judgment, and merging it
is that person's call (this mirrors the `pg-pr` rule that agents never run `pg-pr pr merge`).
A local ff-merge is different — it's your own linear history on your own checkout, so landing
it autonomously is exactly the habit this skill automates.

The mechanics of each flow — exact commands, the bitbucket mirror, coordinated workforest
teardown, conflict handling — live in **`references/integration-and-cleanup.md`**. Read it
before running the integrate/cleanup phases.

## Where captured work goes: beads, or a markdown handoff

Recording what's done, discovered, and unfinished is the memory that survives the session — but
_where_ it's recorded is detected, not assumed. Two backends:

- **Beads-backed repo** — beads is available and scoped to this repo (a `.beads/` directory, or
  `bd list` returns this repo's issues). Work is captured as bd issues and the next-session
  pointer is a single P0 bead. This is the default path throughout the phases below.
- **No-beads repo** — no beads for this repo (`bd` is unconfigured/unavailable and there's no
  `.beads/`). Work is captured in a **markdown handoff doc committed to the repo** — a TODO/handoff
  file that plays the exact role a bead would. Everything else — commit, gates, per-repo
  integration, branch/worktree retirement — is **identical**; only the work-capture medium changes.

Detect this per repo (a `pn` workspace can mix both). Wherever the phases below say "file a
bead," "close a bead," or "leave a P0 bead," a no-beads repo does the markdown-handoff equivalent
described in **"Markdown handoff doc (no-beads repos)."**

## The sequence

Run these in order. Earlier phases are read-only or reversible; the irreversible ones come
last and only after the gates pass.

### 1. Take stock (read-only)

Build the in-scope set per "Why scope is the whole game" above. For each in-scope repo,
capture: current branch, worktree kind, dirty state, commits ahead of `main`, detected flow.
Identify the repo's quality-gate commands (see phase 3). Produce nothing destructive here —
this is the picture everything else acts on.

### 2. Capture the work: close what's done, record what isn't

Your work-tracker — beads, or a markdown handoff doc in a no-beads repo — is the memory that
survives the session, so get it accurate before tearing anything down. In a **beads-backed repo**
this means the `bd` commands below; a **no-beads repo** does the same three things in the markdown
handoff doc instead (see the note at the end of this phase).

- **Close completed work.** For each in-scope bead whose work is actually finished and
  committed: `bd close <id> [<id>...] --reason="..."`. Don't close a bead whose code didn't
  pass gates or didn't land.
- **File discovered work.** Anything you found this session that isn't done — a follow-up, a
  TODO, a bug you noticed, a deferred cleanup — gets a bead so it isn't lost:
  `bd create --title="..." --description="why this exists + what to do" --type=task|bug|feature -p <0-4>`.
- **File unfinished work.** If an in-scope task is partially done, leave a bead describing
  what's left (or update the existing one's notes), so the next session starts from truth.

Keep this lightweight — you're recording reality, not grooming the backlog (that's the
`bead-grooming` skill). Don't write acceptance criteria here; just capture enough that the
work is findable and the intent is clear.

**No-beads repos** do the same three things in the repo's markdown handoff doc instead of bd
issues. Completed work needs no issue to close — it's just committed. Discovered and unfinished
work each become a checklist entry under the doc's "Outstanding / next" section (see "Markdown
handoff doc (no-beads repos)"). The doc is committed with the session's changes in phase 4, so it
lands with the work it describes.

### 3. Quality gates

Run the repo's tests, linters, and build/check before integrating. Discover the commands from
the repo, in this rough order of authority:

- A `justfile` / `Makefile` target (`just check`, `just test`, `make check`).
- Repo convention from `CLAUDE.md` / `AGENTS.md`. (For the `nix-*` repos here that's
  `prek run --all-files` or `pre-commit run --all-files`, then `nix flake check`.)
- Language defaults (`go test ./...`, `pytest`, `npm test`, `cargo test`).

**If a gate fails, stop before integrating.** Don't merge or push code that doesn't pass.
Instead: leave the branch and worktree intact, file a P-high bead capturing the failure
(command + the relevant output), and route this into the next-session handoff (phase 7) so
the resume prompt explains exactly what's red. A failed gate turns a "done" wrapup into a
"paused" one — that's the correct outcome, not a reason to push anyway.

### 4. Commit; leave the tree clean

For each in-scope repo, commit the session's outstanding changes with a clear message. The
end state is a clean working tree for everything in scope. If there are changes you can't
confidently attribute to this session, don't sweep them into a commit — leave them and report
them (scope rule).

End git commit messages with the trailer:

```
Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```

### 5. Integrate (per detected flow)

For each in-scope repo, follow its detected flow from "Reading the terrain", using the
commands in `references/integration-and-cleanup.md`:

- **Local ff-merge:** rebase the branch onto fresh `main`, fast-forward merge into `main`.
  That ff-merge _is_ the integration — **do not push**. If the rebase hits a non-trivial
  conflict, **stop** for that repo — don't force it; keep the branch and roll it into the
  handoff. (If the `merge-executor` agent is available you may dispatch it for this
  rebase+ff-merge+cleanup step; it has the same stop-on-conflict contract.)
- **PR flow:** push the branch to its remote and open or update the PR (prefer the `pg-pr`
  CLI where present). Do not merge it.
- **On `main`:** commit only — nothing to merge or push.

When you do push (PR flow only), push just the one branch for the repo this session touched —
never `pn workspace push` (which hits untouched repos and violates scope).

### 6. Clean up — only what's spent

Cleanup is the most destructive phase, so it's gated on "this work is truly done and landed."

- **Local ff-merge repos** whose branch is now merged into `main`: retire the worktree, then
  delete the branch, then prune. For a standalone worktree: `git worktree remove` +
  `git branch -d`. For a `pn` coordinated set: only `pn workspace workforest remove` the set
  once **every** repo in the set is clean and integrated — otherwise keep it and note which
  repo blocks teardown.
- **PR-flow repos:** keep the branch and worktree — the work isn't merged yet. They get
  retired in a future wrapup after the PR lands.
- **Stashes:** clear only stashes this session created. Pre-existing stashes are out of scope;
  leave them and list them.

Details and the exact command order are in `references/integration-and-cleanup.md`.

### 7. Decide done vs. more-work, and hand off

The session is "done" only if the workspace is genuinely at rest. Decide it with one question:
**when someone sits down next, is there anything to pick up?** If yes — for any reason — leave a
single P0 "continue here" bead pointing at it. If there's truly nothing, don't.

There's something to pick up when either holds:

- **Interrupted or blocked state:** gates failed, a rebase/merge was stopped, a PR is open but
  unmerged, or an in-scope task is unfinished or blocked.
- **Deferred or discovered work you mean to continue:** follow-ups you consciously pushed to
  "next session," or things you found in phase 2 that carry this thread forward. This counts
  _even when everything you committed landed clean and green_ — a tidy tree is not the same as no
  next work, and missing it is the easy mistake: the session ends green, the follow-ups are real,
  and nobody writes the pointer, so the next session starts cold. The test is intent, not
  inventory — phase 2 files _every_ loose end, but the P0 is for what you actually mean to resume.
  A genuinely separate tangent you don't plan to pick up next stays a standalone backlog bead at
  its own priority; it doesn't by itself become the continuation pointer. When in doubt, write
  the pointer.

When there's something to pick up:

- Keep the relevant branch(es) and worktree(s) (phase 6 already did, for PR/blocked cases).
- Write **one P0 bead** — the single next-session entry point. A cold-start brief: where the
  work stands, which branch/worktree to resume in, what's red/unmerged/deferred, and the first
  concrete step. The discovered/deferred beads from phase 2 keep their own (non-P0) identities;
  this P0 _links_ them and names the one place to start. One pointer, not many — its job is to
  resume fast and keep the next session from fanning out into parallel threads. Format in
  "Next-session handoff bead" below.
- If a prior wrapup already left a P0 "continue here" bead for this thread, update it instead of
  filing a second (see "Safety and idempotency").

**Truly done** — and only then skip the P0 — means all in-scope work is committed, gated green,
integrated (merged locally or PR opened with nothing else pending), branches/worktrees retired,
_and_ nothing deferred or discovered that you mean to continue. Note completion in the summary.
When you can't tell which side of the line you're on, write the pointer: a redundant P0 costs one
line to close, a missing one costs the next session a cold restart.

**No-beads repos:** the continuation pointer is the markdown handoff doc, not a P0 bead. When work
carries over, write (or refresh) the doc's top "Resume here" section as the single next-session
entry point, exactly as the P0 bead would be — one front door, updated in place on a re-run rather
than duplicated. Its "Outstanding / next" checklist holds the discovered/deferred items that would
otherwise be non-P0 beads. See "Markdown handoff doc (no-beads repos)." Skip writing it only when
nothing carries over at all.

(There's no separate beads "sync" step: in server mode `bd create`/`bd close` write straight
to the shared remote, so the housekeeping in phase 2 is already persisted.)

## Next-session handoff bead

When work remains, capture a resume brief as a single P0 bead. The body should let a fresh
session pick up cold without re-deriving context:

```bash
bd create --type=task -p 0 \
  --title="Resume: <short description of the work>" \
  --description="$(cat <<'EOF'
## Where this stands
<1-3 sentences: what got done this session, what's left>

## Resume here
- Repo / worktree: <path or branch to check out>
- State: <branch ahead of main by N, PR #NN open, gates red, etc.>
- First step: <the concrete next action>

## Watch out for
<gate failures, stopped rebase/conflict, decisions still open>
EOF
)"
```

One P0 bead, not many — it's the single entry point for the next session, created whenever any
work carries over (interrupted, deferred, or discovered). The other follow-ups from phase 2 keep
their own (non-P0) beads; this P0 doesn't replace them — it points at the one place to start and
links them, so the next session sees a single front door instead of a scattered backlog.

## Markdown handoff doc (no-beads repos)

When a repo doesn't use beads, the same recording happens in a markdown file committed to the
repo — one doc that captures both the loose ends (phase 2) and the single next-session entry point
(phase 7). It plays the role beads would: durable, findable, version-controlled memory that
survives the session.

**Where it lives.** Reuse the repo's existing handoff/TODO doc if it has one (e.g. `HANDOFF.md`,
`TODO.md`, `docs/handoff.md`); otherwise create `HANDOFF.md` at the repo root. One doc per repo —
don't scatter. Commit it in phase 4 with the session's changes so it lands with the work it
describes.

**Shape.** Keep it scannable and dated so a fresh session can resume cold. Add a new dated section
for this session's carry-over rather than overwriting prior history (unless that history is now
stale):

```markdown
# Session handoff

## <YYYY-MM-DD> — <short description of the work>

### Where this stands

<1-3 sentences: what got done this session, what's left>

### Resume here

- Repo / worktree: <path or branch to check out>
- State: <branch ahead of main by N, PR #NN open, gates red, etc.>
- First step: <the concrete next action>

### Outstanding / next

- [ ] <discovered follow-up, deferred cleanup, or unfinished task>
- [ ] <bug you noticed>

### Watch out for

<gate failures, stopped rebase/conflict, decisions still open>
```

This mirrors the P0 handoff bead: "Where this stands" + "Resume here" + "Watch out for" are the
cold-start brief, and the "Outstanding / next" checklist holds the discovered/unfinished items
that would otherwise be their own non-P0 beads. One doc, not many — it's the single front door.

When a re-run finds an existing handoff doc for still-open work, **update its top section** rather
than appending a duplicate — the next session needs one front door, same as the "update the P0
rather than file a second" rule for beads.

## Safety and idempotency

- **Re-running is safe.** A second wrapup with nothing new to do should find a clean tree,
  no in-scope unmerged work, and simply report "nothing to wrap up." If a prior wrapup already
  left a P0 "continue here" bead for work that's still open, update that bead rather than filing
  a duplicate — the next session needs one front door, not a stack of them.
- **Leave a P0 whenever work carries over.** Deferred, blocked, or unfinished in-scope work — or
  discovered work you mean to resume next — means there's a next session; capture it as the
  single P0 pointer (linking the rest). Skip the P0 only when nothing carries over at all. When
  unsure, write it.
- **No-beads repos use the markdown handoff doc.** Everywhere these rules say "file/close/update a
  bead" or "leave a P0," a no-beads repo does the markdown-handoff-doc equivalent (see "Markdown
  handoff doc (no-beads repos)"). The intent — one durable, committed front door, updated in place
  rather than duplicated — is identical.
- **Never touch out-of-scope work** — unrelated branches, others' worktrees, pre-existing
  stashes, dirty files you didn't create. Skip and report.
- **Never auto-merge a PR.** Pushing + opening/updating is the boundary.
- **Never land red code.** A failed gate stops integration for that repo.
- **Never `pn workspace push`/`rebase`** for a scoped wrapup — they hit every repo. Use
  per-repo git.
- **Don't reconfigure beads to local.** Beads writes go to the shared remote automatically in
  server mode; if beads access fails, stop and surface it rather than switching to local
  (project rule).
- **Stop on non-trivial merge conflicts** — don't force-resolve; keep the branch and hand off.

## End-of-run summary

Close with a compact report the user can scan without opening anything:

```
Session wrapup — <standalone | pn workspace>

Integrated:
| repo      | flow         | result                                  |
|-----------|--------------|-----------------------------------------|
| homelab   | local merge  | feat-x → main (ff-merge); worktree removed |
| nix-personal | PR        | branch pushed, PR #42 updated (unmerged) |

Beads: closed 3 (tc-12, tc-13, tc-15); filed 2 (tc-88 follow-up, tc-89 bug).
Next session: P0 tc-90 — resume nix-personal PR #42 after review.

Left untouched (out of scope):
- homelab branch fix-y (not this session)
- 1 pre-existing stash in nix-overlay
```

For a no-beads repo, replace the Beads / Next-session lines with the handoff doc, e.g.
`Handoff: HANDOFF.md updated — 2 outstanding items; resume brief for feat-x.`

If nothing was in scope, say so plainly rather than inventing work.

## Command quick reference

| need                            | command                                                                        |
| ------------------------------- | ------------------------------------------------------------------------------ |
| in-progress beads               | `bd list --status in_progress`                                                 |
| PR-tracker beads                | `bd list --type=merge-request`                                                 |
| close finished work             | `bd close <id> [<id>...] --reason="..."`                                       |
| file discovered/unfinished      | `bd create --title=... --description=... --type=... -p <0-4>`                  |
| dirty state                     | `git status` ; ahead of main: `git log main..`                                 |
| detect PR (per repo)            | `gh pr view` / `pg-pr pr show` / `bd list --type=merge-request`                |
| run gates (nix-\* repos)        | `prek run --all-files` (or `pre-commit run --all-files`); `nix flake check`    |
| local ff-merge + cleanup        | see `references/integration-and-cleanup.md`                                    |
| push branch (PR flow only)      | `git push -u origin <branch>` (NOT `pn workspace push`)                        |
| remove pn workforest set        | `pn workspace workforest remove <branch>` (only when whole set clean)          |
| prune stale worktree admin      | `pn workspace workforest prune`                                                |
| next-session handoff            | one P0 `bd create` (see "Next-session handoff bead")                           |
| record work (no-beads repo)     | append to the repo's handoff doc (see "Markdown handoff doc (no-beads repos)") |
| next-session handoff (no-beads) | update the handoff doc's top "Resume here" section                             |
