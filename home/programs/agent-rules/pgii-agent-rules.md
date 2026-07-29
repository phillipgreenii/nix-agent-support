# Rules

> The section `## Rules for Interactive Sessions Only` applies only when working with the user directly.
> Autonomous agents invoked via `claude -p` (e.g. background workers, polecats, dogs)
> MUST ignore that section and apply only the rules under `## Always-Apply Rules`.

## Always-Apply Rules

### Design & Documentation Standards

- MUST use design pattern terminology when discussing designs
- MUST use separate code blocks per file in markdown-supporting files
- MUST write policies using RFC 2119 language (MUST/SHOULD/MAY/etc.)
- MUST use mermaid diagrams instead of images in documentation

### Workflow Sequence

1. **Search First** — confirm functionality exists or doesn't before implementing
2. **Reuse First** — extend existing code/patterns before creating new; minimize changes
3. **No Assumptions** — only use files read, user messages, tool results. IF missing info: search first, then ask
4. **Challenge Approach** — identify and state flaws/risks/better approaches directly

### Development Standards

#### Validation

**CRITICAL**: Before claiming any change is complete:

- If the project has `.pre-commit-config.yaml`: `pre-commit run --all-files` MUST pass
- If the project has `flake.nix`: `nix flake check && darwin-rebuild check --flake .` MUST pass
- IF no tests exist for changed code: create them
- NEVER claim code is complete without passing tests

#### Structured Data Files

MUST use `jq`/`yq`/`tq` for JSON/YAML/TOML manipulation over text-based editing (sed, awk, python).

#### Unit Tests

MUST be isolated; if they modify files directly, the test MUST generate the scenario in a temp directory.

### Beads Claim Hygiene

> `bd` has NO `unclaim` verb. A release MUST be synthesised, and the `--assignee ""` half is
> the one that gets forgotten. A bead left `status=open` with a non-empty `assignee` is
> **stranded**: `bd ready --claim` correctly skips it (it is claimed), `bd update <id> --claim`
> rejects it ("issue already claimed by …"), and no stale-`in_progress` sweep can see it —
> so it sits at the top of the queue, unclaimable and invisible.

- **B-1** Whatever claims a bead MUST release it. Every exit path — success, hand-back, park,
  escalate, defer, give up, out of context — MUST end with the bead either `closed` or
  released. MUST NOT end a session still holding a claim.
- **B-2** A release MUST clear the assignee, not just the status:
  `bd update <id> --status open --assignee ""`. `--status open` alone is NOT a release.
- **B-3** The status change and the assignee clear MUST be a SINGLE `bd update` call. Two
  calls leave a window in which the bead is `open` but still claimed.
- **B-4** Any transition out of `in_progress` that is not a `bd close` MUST clear the assignee
  — including `blocked`, `deferred`, and re-`open`. A `bd close` MAY leave the assignee (it
  records who did the work), so anything that later RE-OPENS a closed bead MUST clear it then.
- **B-5** MUST prefer an explicit `--actor "<session-id>"` on every claim. Without it the
  assignee resolves to the human's display name, which makes an abandoned claim look like the
  operator deliberately took the bead.
- **B-6** On finding a bead that is `open` with a non-empty assignee, an agent MUST report it
  rather than silently steal or clear it — it is this defect, and the operator decides.

### Handoff Preconditions

> A precondition written at time T against implementation I persists as an INSTRUCTION while
> I changes. Phrased against the MECHANISM ("is it a shell function?", "is it in this file?")
> it rots silently at the next refactor: the mechanism is gone, so the check can NEVER pass,
> and every later reader concludes "not applied yet". If its failure branch says "wait / try
> again later", the rot becomes a NON-TERMINATING LOOP instead of a visible failure.

- **P-1** A precondition or verification step written for a LATER reader (park/handoff
  comment, issue note, runbook step) MUST be stated as an OBSERVABLE OUTCOME — what a reader
  can run and see. It MUST NOT be stated as an implementation MECHANISM (which file defines
  it, function vs. `PATH` command, which module it lives in).
- **P-2** It MUST cite the provenance it was derived from — commit plus the file path(s)
  actually read — so a later reader can re-derive it and detect the drift.
- **P-3** A reader MUST re-derive a precondition from CURRENT source before acting on it and
  MUST NOT trust the text's mechanism claim. If the cited mechanism no longer exists, the
  precondition is UNSATISFIABLE, not unmet: the agent MUST report it as suspected-stale
  rather than treat it as "not ready yet".
- **P-4** "Precondition unmet" MUST NOT be a non-terminating outcome. Guidance whose failure
  branch is "wait / retry / re-park" MUST bound the repeats and MUST name the escalation.
- **P-5** The SECOND time the SAME precondition blocks the SAME unit of work, the
  precondition itself MUST be escalated as suspected-stale; the agent MUST NOT block on it a
  third time.

### Premise Freshness

> An issue body is a SNAPSHOT of the world at filing time; a park comment is a snapshot at
> parking time. Both keep reading as CURRENT while the world moves. Observed 2026-07-27: in
> one pass over the parked `human` queue, 5 of 9 beads were already resolved or void —
> commits had landed, Jira issues had closed, referenced modules had been deleted. Re-parking
> such a bead inflates the queue and asks the operator to adjudicate a non-question. ACTING on
> one is worse: an ADR draft was one approval away from landing as "Accepted" while
> prescribing edits to two module trees that had already been deleted and unified elsewhere.
> One `git ls-tree` on the two paths it named would have settled it.

- **F-1** Before parking, re-parking, escalating, releasing, or ACCEPTING work whose premise
  was recorded EARLIER than now, an agent MUST re-verify that premise against CURRENT reality,
  and MUST record the check where the next reader will see it. Work MUST NOT be parked,
  re-parked, or accepted on an unverified premise.
- **F-2** The check MUST be mechanical and cheap — the probes in F-3, run verbatim. It MUST
  NOT be a judgement about whether the recorded text "still looks right".
- **F-3** For each external referent the premise NAMES — a commit, an external ticket, a
  file/module/path, a code symbol, a sibling bead, a derived identifier — the agent MUST run
  the matching NAMED PROBE and MUST record its decisive output verbatim. `main` below means
  the repo's primary branch; run each from the repo in question.

  | Probe                  | Command                                                                                                                             | ⇒ STALE (premise moot / recorded value wrong)                                                                                    | ⇒ STILL LIVE (as recorded)                                                  |
  | ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
  | **`landed?`**          | `git merge-base --is-ancestor <sha> main; echo $?`                                                                                  | `0` — the commit IS on main, so "maybe still unlanded" is already answered                                                       | `1` — genuinely not on main                                                 |
  | **`pushed?`**          | `git fetch --quiet origin && git branch -r --contains <sha>`                                                                        | any output (e.g. `origin/main`) — already pushed, so "close once the push happens" is satisfied                                  | EMPTY output. Read the OUTPUT, not `$?` — the exit status is `0` either way |
  | **`patch-identical?`** | `git cherry -v main <branch>`                                                                                                       | no output, or every line starts with `-` — an equivalent patch is already upstream under a DIFFERENT sha, so the branch is spent | any line starting with `+` — that commit is genuinely not upstream          |
  | **`path-exists?`**     | `git ls-tree -r --name-only main -- <path> [<path>…]`                                                                               | a path the plan edits is ABSENT from the output — that module is GONE, so a design prescribing edits to it is void               | every named path echoes back. Read the OUTPUT, not `$?`                     |
  | **`symbol-shape?`**    | `git grep -c -- '<symbol>' main -- <path>`                                                                                          | exit `1`, no output — the option/function/field the steps operate on no longer exists at that path                               | exit `0` with `main:<path>:<n>` — still present                             |
  | **`ticket-open?`**     | `pjira issue <KEY> \| jq -r '.status'`                                                                                              | `Closed` / `Done` / `Resolved` — the external work finished, so "continue `<KEY>`" is moot                                       | anything else. `pjira`'s JSON is FLAT: `.status`, never `.fields.status`    |
  | **`sibling-open?`**    | `bd show <sib-id> --json \| jq -r '.data[0].status'`                                                                                | `closed` — the bead this one waits on, duplicates, or defers to is done                                                          | `open` / `in_progress` / `blocked`                                          |
  | **`next-free-id?`**    | `printf '%04d\n' "$(( 10#$(git ls-tree -r --name-only main -- docs/adr \| rg -o '/(\d{4})-' -r '$1' \| sort -n \| tail -1) + 1 ))"` | DIFFERS from the number the draft recorded — that id is TAKEN by someone else; renumber before landing                           | equals the recorded number                                                  |

- **F-4** A probe reading is decisive ONLY when it resolves. `fatal: Not a valid commit name`
  (exit `128`) means this clone does not know that sha; a missing repo, an unreachable ticket,
  or a referent the premise never names precisely enough to probe are all the same case: the
  agent MUST read it as STILL LIVE and MUST NOT call the premise moot. Ambiguity is never
  evidence of mootness.
- **F-5** If the premise names NO external referent, the agent MUST record that fact
  explicitly rather than skip the step silently — an unrecorded check is indistinguishable
  from a skipped one.
- **F-6** REVIEW QUALITY IS NOT A STALENESS SIGNAL. That a draft/plan was adversarially
  reviewed, had findings fixed, or had its details verified against live source says only that
  it was accurate WHEN WRITTEN; a thorough review of a snapshot ages exactly as fast as the
  snapshot. An agent MUST NOT accept "it was already reviewed", "it looks plan-ready", or an
  approving review verdict in place of running the F-3 probes.
- **F-7** A premise proven moot MUST terminate in a CLOSE, not another park/re-park/release —
  but CLOSE-AS-SUPERSEDED MUST EXTRACT, NOT DISCARD. Before closing, the agent MUST read the
  stale work and, if it makes a claim that CURRENT source VIOLATES (a defect it predicted, a
  decision it called load-bearing that the shipped version skipped), MUST file that as its own
  issue linked back to the original (`bd create … --deps "discovered-from:<id>"`) and MUST
  name the new id in the close reason. A blind close is forbidden.
- **F-8** A DERIVED IDENTIFIER recorded at draft time (next-free ADR number, sequence id,
  "highest accepted is N") MUST be recomputed at land time and MUST NOT be trusted as
  recorded; between drafting and landing, someone else takes it.

### Worktree-Review Label Lifecycle

> `worktree-review` is a MARKER, not a property: it says "an isolation artifact exists whose
> disposition only a person may rule on". It is also `/unblock-human-beads`' triage class-1
> trigger, so while it is attached the bead is substrate-class and is never released to
> `/drain-beads`. Observed 2026-07-27: NOTHING ever removed it. `pg2-zxql0`, `pg2-s5yat` and
> `pg2-spwj9` had it stripped BY HAND after the operator ruled, precisely so the next sweep
> would not re-park them; `pg2-8u0ul`, `pg2-fijqu`, `pg2-kl0o4` and `pg2-6laiy` were CLOSED
> still carrying it. The label also has a priority side effect — those beads' notes read
> `Promoted P2->P0` / `Promoted P3->P0` — and nothing undid that either, so `pg2-spwj9` holds
> P0 for one dirty `flake.nix`. A marker with no exit condition guarantees both a repeat
> operator prompt and a re-park on every later pass. There is NO committed sweep that applies
> this label; every application so far came from an ad-hoc agent session, which is why its
> contract lives HERE, in the always-on rules, and not in a script — these rules bind any
> agent or future sweep that touches the label.

- **W-1** `worktree-review` MUST be applied only when an ISOLATION ARTIFACT exists whose
  keep-vs-discard a person must rule on: a dirty worktree, unlanded commits on a
  `drain/<id>` branch, or a workforest set / `.worktrees/*` entry of unknown disposition. It
  MUST NOT be used as a generic "needs a human" marker — `human` is that marker. It MUST be
  applied TOGETHER with `human` in the same update: `/drain-beads` excludes only `human` from
  its claim query, so a `worktree-review`-only bead (e.g. `pg2-8u0ul`) is drain-claimable and
  the class-1 substrate guard never sees it.
- **W-2** Applying the label MUST, in the SAME `bd update`, record an ENTRY MARKER in `notes`
  naming the isolation, its location, and the question a person must answer — and MUST state
  the priority effect in exactly one of two parseable forms: `Promoted P<prior>->P<new>.` or
  `No promotion (priority left at P<n>).` An unrecorded promotion is unrestorable (W-7), and
  an unrecorded absence is indistinguishable from a forgotten record.

  ```bash
  bd update <id> --add-label human,worktree-review --priority 0 \
    --append-notes "[worktree-review $(date +%F)] <what isolation, where, and what a person must decide>. Promoted P<prior>->P0." \
    --actor "ID"
  ```

- **W-3** A bead that ALREADY carries `worktree-review` MUST NOT be re-labeled or re-promoted
  — so at most one unresolved promotion record ever exists and this read-back is unambiguous
  (verified: prints `Promoted P3->P0` for `pg2-spwj9`, `Promoted P2->P0` for `pg2-6laiy`):

  ```bash
  bd show <id> --json | jq -r '.data[0].notes // ""' | rg -o 'Promoted P[0-9]->P[0-9]' | tail -1
  ```

- **W-4** The label's EXIT CONDITION is a RECORDED VERDICT on the isolation — which of
  keep / fix-forward / discard / tear-down applies, plus what was done and what remains. "A
  person looked at it" is not a verdict; it MUST be written on the bead. Until a verdict is
  recorded the label MUST stay.
- **W-5** Once the verdict is recorded, the label MUST be removed AND the recorded priority
  restored in the SAME update that releases the bead. `bd update` accepts `--remove-label`,
  `--priority`, `--status`, `--assignee` and `--append-notes` together, so a RELEASE is ONE
  call and leaves no window:

  ```bash
  bd update <id> --remove-label human,worktree-review --priority <prior> --status open --assignee "" \
    --append-notes "[worktree-review-resolved $(date +%F)] <verdict>. Restored P0->P<prior>." --actor "ID"
  ```

- **W-6** `bd close` accepts NEITHER `--remove-label` NOR `--priority`, so a CLOSE cannot be
  atomic. The cleanup MUST be written FIRST, as its own `bd update`, and that write MUST NOT
  drop `human`:

  ```bash
  bd update <id> --remove-label worktree-review --priority <prior> \
    --append-notes "[worktree-review-resolved $(date +%F)] <verdict>. Restored P0->P<prior>." --actor "ID"
  bd close <id> --reason "<verdict + isolation disposition>" --actor "ID"
  ```

  The order is load-bearing. Interrupted after the first write, the bead is clean, correctly
  prioritised, still open and still `human` — it returns to the human queue and the next pass
  simply closes it. Interrupted in the reverse order it becomes exactly the observed defect: a
  CLOSED bead carrying the label at a promoted priority, which no queue ever revisits, so the
  residue is permanent.

- **W-7** If the W-3 read-back finds NO `Promoted` token, the pre-promotion priority is
  unrecoverable from the bead. The agent MUST NOT guess a value, MUST NOT silently leave the
  promoted value unremarked, and MUST NOT withhold the label removal (withholding it re-creates
  the very defect). It MUST remove the label, leave the priority UNCHANGED, record the gap in
  that same update — `NO promotion record; priority left at P<n> — unverified.` — and, because
  class 1 already puts the operator in the loop, ASK the operator for the correct priority in
  that same exchange and use their answer. Only an operator who declines leaves it at P<n>.
- **W-8** Clearing the label MUST NOT be used to make a bead drain-eligible while substrate
  work REMAINS. The class-1 guard triggers on the label OR on the work itself, so the label is
  a SUFFICIENT trigger and never a necessary one — removing the marker cannot let a genuinely
  substrate-mutating bead escape the guard. If the verdict is that isolation must still be torn
  down or pruned, the label MUST stay and the terminal action MUST be an in-session CLOSE with
  the operator, or a DEFER — never a RELEASE. A DEFER MUST keep BOTH the label and the promoted
  priority: the question is still open.

### Unpushed Landing Debt

> A local ff-merge makes work LANDED, not PUBLISHED. An agent loop that lands locally is correctly
> forbidden from pushing, so every unit it lands adds commits to local `main` that no session is
> accountable for publishing. Observed 2026-07-27: the only handle for a whole batch push was
> `pg2-5subz` — a wrap-up P0 handoff bead whose description said the push was "covered by an
> existing P0 front-door bead" that no longer existed. Closing it (its OWN 3 commits were verified
> pushed) would have orphaned 11 unrelated unpushed commits. `pg2-dawg2` was filed to catch those
> and was legitimately closed 2026-07-28 having pushed all 12 — its close reason reads "every repo
> 0 ahead / 0 behind / clean, and `pn workspace doctor` reports no errors". By 2026-07-29 the same
> workspace was 19 commits unpushed again (1 repo-base + 2 ziprecruiter + 15 agent-support +
> 1 overlay) across 4 repos, with no handle at all. So the defect is not merely "a bead can be
> closed while still true": the debt REGENERATES on every land, and a bead can only ever describe
> ONE INSTANT of it. The truth is already in git. What was missing was never a RECORD — it was an
> OBLIGATION TO LOOK.

- **U-1** Unpushed landing debt MUST be treated as DERIVED STATE and re-derived from git at the
  moment it matters. No bead, label, comment, or handoff doc is its handle.
- **U-2** An agent MUST NOT create, maintain, or "restore" a standing push bead, a
  `push-carryover` bead, or a handoff-doc section whose PURPOSE is to remember that locally landed
  commits are unpushed. Such a record duplicates computable state, goes stale at the very next
  land, and can be closed while the condition it names is still true — that IS this defect. A bead
  for ONE push a person has already authorized as a discrete task is NOT this; making a bead the
  standing accounting for the aggregate debt IS.
- **U-3** In a `pn` workspace the probe is `pn workspace doctor` — it already computes
  `origin/<branch>` vs local for EVERY repo, so an agent MUST reuse it rather than write another
  per-repo `git rev-list --count origin/main..main` loop. Run it from anywhere inside the
  workspace:

  ```bash
  pn workspace doctor
  ```

  Read it as follows. Both readings are verified against live output on 2026-07-29:
  - **Debt present** — a `branch-synced` ERROR whose message carries the count. `ahead N` with
    `N > 0` IS the unpushed debt; `behind M` alone is NOT (that is un-pulled remote work):

    ```text
    ERROR branch-synced   repo "phillipgreenii-nix-agent-support" local HEAD 2c838e0 != remote e9fed09 (ahead 15, behind 0) [fixable]
    ```

  - **No debt for a repo** — the repo emits NO section at all. Verified: `-nix-personal` and
    `-nix-support-apps` both report `0` from `git rev-list --count origin/main..main` and are
    absent from the doctor output entirely. Absence of a repo is a PASS, not a skip.
  - **No debt anywhere** — the trailer reads `workspace doctor: 0 errors, 0 warnings.` with no
    `branch-synced` line. (`pg2-dawg2`'s close reason records observing exactly this after its
    push.) The trailer count alone is NOT sufficient: other checks (e.g. `tree-clean`) also raise
    errors, so the agent MUST read the `branch-synced` lines specifically.
  - Machine-readable form, when the count must be extracted rather than eyeballed — the count
    lives in `.message`, not in its own field:

    ```bash
    pn workspace doctor --json |
      jq -r '.findings[] | select(.check == "branch-synced")
             | "\(.repo) ahead=\(.message | capture("ahead (?<a>[0-9]+)").a)"'
    ```

- **U-4** The probe MUST be run READ-ONLY. An agent MUST NOT pass `--fix`. Verified with
  `pn workspace doctor --fix --dry-run`: the plan for `branch-synced` is
  `git merge --ff-only origin/<branch>` executed in the CANONICAL clone, which cannot publish an
  ahead-only divergence (so it does not clear the debt) and mutates the canonical clone, which
  **R-3** forbids.
- **U-5** Discharging the debt is OUTWARD-FACING and operator-authorized. An agent MUST NOT
  `git push`, `pn workspace push`, `pn workspace update`, or `pn workspace apply`, and MUST NOT
  invoke `/pn-workspace-sync` or `/pn-workspace-update`, on its own initiative to clear it.
  REPORTING is in scope; PUBLISHING is not.
- **U-6** A session that landed anything locally MUST run the U-3 probe before it terminates, and
  when any `ahead N > 0` it MUST report to the operator, in its terminal report: the `branch-synced`
  lines VERBATIM, the total with its per-repo addends shown, and the sanctioned remediation path.
  Naming the path is mandatory — a bare count is an observation, not a handoff. The path, as
  actually carried out by `pg2-dawg2` on 2026-07-28: rebase each repo onto its remote primary
  branch, then `pn workspace update --siblings-only` (relocks sibling inputs and pushes), then
  `pn workspace push` to confirm `Everything up-to-date`, then re-run `pn workspace doctor` and
  expect no `branch-synced` errors. The `/pn-workspace-sync` skill does the same fetch + rebase +
  validate + land + push in an isolated workforest.
- **U-7** `pn workspace doctor` and the **F-3** `pushed?` probe are NOT interchangeable. Doctor is
  the workspace-wide AGGREGATE ("does any repo hold unpublished commits right now"); `pushed?` is
  the per-COMMIT refinement ("is THIS sha on a remote"). An agent MUST use doctor for the debt
  question and `pushed?` when one commit's published-ness decides a premise, and MUST NOT
  generalize a single `pushed?` reading to "that repo is pushed".
- **U-8** A "no debt" reading is valid only for the instant it was taken (**F-1**). It MUST NOT be
  cached across a later land, a peer session, or a hand-off — the next reader MUST re-run the probe
  rather than trust a recorded count.

### General Guidelines

- Before recommending paid/licensed software, confirm the cost with the user.

### Git Workflow

- Always commit to the correct branch. Before committing, run `git branch --show-current` to verify. If changes were made on the wrong branch, alert the user before proceeding.
- When pre-commit hooks exist, always run `git diff --cached` and address any formatting/lint issues before attempting to commit. If subagents generate changes, ensure files are properly staged.

### Git Worktree / Integration Discipline

> The "primary branch" is the repo's default integration branch, resolved as
> `pgii-integrate-branch.primaryBranch` (git config) → `git symbolic-ref refs/remotes/origin/HEAD`
> (git standard) → `main`.

- **R-1** The canonical clone MUST have its primary branch checked out as steady state.
- **R-2** Only the canonical clone MAY have the primary branch checked out; a worktree/workforest member MUST use a feature branch.
- **R-3** An agent MUST NOT switch the canonical clone off its primary branch or leave it dirty in steady state. On finding it unexpectedly off-branch/dirty, the agent MUST stop and report — not reset, re-checkout, stash, or work around it.
- **R-4** By default an isolated single-repo change MUST be done in a git worktree.
- **R-5** The worktree (R-4) and workforest requirements MAY be overridden when the user explicitly says so.
- **R-6** For a change judged very small/quick, the agent MAY take the direct-commit path (commit on the primary branch in the canonical clone) — but if it does, it MUST first ask the user.
- **R-7** Concurrent agents in different worktrees are expected; the primary branch advancing during work is absorbed by the rebase. Only a rebase conflict or a persistent ff-race during landing warrants attention.
- **R-8 (floating-branch halt)** If an integration would advance the canonical primary branch (e.g. a local ff-merge) and the canonical clone is not on its primary branch, the agent MUST halt and report — merging then advances the wrong branch and orphans work into hanging branches. (For methods that do not touch the canonical primary — e.g. `pull-request` — an off-primary/dirty canonical is an R-3 anomaly to surface, not necessarily to halt.)
- **R-9 (integration entry point)** To integrate completed work, the agent MUST use the `integrate-branch` skill. The agent MUST NOT use `superpowers:finishing-a-development-branch` (plain non-ff merge, no rebase).

### Prohibited Actions

#### System Commands

- **CRITICAL**: NEVER run system activation commands (e.g., `darwin-rebuild switch`) without explicit user request — these are user-only commands
- **CRITICAL**: NEVER use `sudo`
- When building/validating nix changes without activation, use a build-only command

#### Version Control

- Include the Jira issue as `Refs: TICKET-ID` on the line immediately after the subject (before the body). Extract the ticket ID from the branch name (format: `username.TICKET-ID.description`). A valid ticket ID matches `[A-Z]+-\d+` (e.g., `FINDEV-9208`, `CI-1494`). If the branch contains `NO-JIRA`, `NOJIRA`, or any variation instead of a real ticket ID, omit the `Refs:` line entirely.
- **CRITICAL**: NEVER use `--no-verify` (or `-n`) on git commands without explicit user approval
- IF git hooks report violations: MUST fix the violations rather than bypassing hooks

#### Numeric Data

- **CRITICAL**: NEVER include calculated numbers without showing calculation method

#### Estimates

- **CRITICAL**: NEVER provide time estimates
- IF signaling effort needed: use t-shirt sizes (S/M/L/XL)

## Rules for Interactive Sessions Only

### Interaction Protocol

- MUST provide direct answers to questions without making code/file changes
- IF question implies work: confirm intent before proceeding
- MUST question assumptions, offer counterpoints, and state problems directly — prioritize correctness over agreement

### Development Standards

#### Planning & Design

- DEFAULT: iterative discussion → plan approval → implementation
- MUST NOT start coding without confirmation
- EXCEPTION: MAY proceed immediately when explicitly provided an implementation plan
- MUST critique non-trivial plans via independent subagent; iterate until no adjustments needed
- IF user input required during critique: ask before continuing
