# Rules

> The section `## Rules for Interactive Sessions Only` applies only when working with the user directly.
> Autonomous agents invoked via `claude -p` (e.g. background workers)
> MUST ignore that section and apply only the rules under `## Always-Apply Rules`.

## Always-Apply Rules

### Design & Documentation Standards

- MUST use design pattern terminology when discussing designs
- MUST use separate code blocks per file in markdown-supporting files
- MUST write policies using RFC 2119 language (MUST/SHOULD/MAY/etc.)
- MUST use mermaid diagrams instead of images in documentation

### Mistake Acknowledgment Marker

> Purpose: make agent self-corrections MECHANICALLY COUNTABLE so their rate can be tracked over
> time. It is a WORDING convention only. Adopted 2026-07-30; it generates data FORWARD ONLY and
> cannot be backfilled, which is why it landed ahead of the tooling that will consume it.

- **M-1** When an acknowledgment of the agent's own error is warranted, its first words MUST be
  `Correction:` — one stem, at the start of the sentence, in user-visible text. Thinking blocks are
  excluded.
- **M-2** M-1 MUST NOT change HOW OFTEN the agent acknowledges anything. The threshold for whether
  an acknowledgment is warranted is set elsewhere and is unchanged: correct an earlier statement
  only when the error would change the user's code, conclusions, or decisions. Silent fixes stay
  silent and MUST NOT be marked. If M-1 would increase acknowledgment frequency, M-1 is being
  misapplied.
- **M-3** The agent MUST NOT add a second phrase distinguishing self-caught from user-caught. That
  provenance is derived from transcript structure (whether the preceding turn was a typed user
  prompt), so stating it is redundant and MUST NOT be attempted.

### Workflow Sequence

1. **Search First** — confirm functionality exists or doesn't before implementing
2. **Reuse First** — extend existing code/patterns before creating new; minimize changes
3. **No Assumptions** — only use files read, user messages, tool results. IF missing info: search first, then ask
4. **Challenge Approach** — identify and state flaws/risks/better approaches directly

### Absolute-Path Provenance

> Observed 2026-07-30 (8-day census, 924 transcripts): 104 of 152 failed Reads named a root that
> does not exist on this machine — 99 `/home/…`, 4 `/mnt/user-data/…`, 1 `/repo/…` — across 86
> distinct sessions, worst single session 3, and 100% in the main loop rather than subagents. In
> the traced cases the task gave repo-RELATIVE paths and the agent, required to use absolute paths,
> FABRICATED a root instead of resolving against the session cwd. The failure text names the real
> cwd, so each one is a round trip spent asking for something the harness had already answered.

- **A-1** An absolute path MUST be built only from a root OBSERVED this session (the env block's
  working directory, a tool result, or the user's text). This machine's roots are `/Users`,
  `/Volumes`, `/nix` and `/private`; `/home`, `/mnt` and `/repo` do not exist on it, so producing
  one means the root was invented rather than observed.
- **A-2** Given a repo-relative path, resolve it as `<session-cwd>/<relative>`. If the root is
  uncertain, probe first (`ls` the parent, Glob the suffix, or `git ls-files -- '*<name>'`) — MUST
  NOT Read a guessed absolute path.
- **A-3** When briefing a subagent, the brief MUST state the absolute repo root once; a brief that
  lists relative paths without a root causes exactly this defect.

### Development Standards

#### Validation

**CRITICAL**: Before claiming any change is complete:

- If the project has `.pre-commit-config.yaml` (test with `test -f .pre-commit-config.yaml && echo yes || echo no` — an exit-0 probe; do NOT probe by running the tool, and do NOT probe with bare `ls`, which exits nonzero on a missing file and is therefore itself a failed tool call — 19 such failures in the 8 days to 2026-07-30): the pre-commit hooks MUST pass on the **changed** files. The **commit's own hook run is the gate** — a `git commit` fires `prek`/`pre-commit` on the staged files (so `git add -A` first, or a generated change escapes the run). To validate before committing, run `prek run --files <the changed files>` (scoped, fast). Do **NOT** use `prek`/`pre-commit run --all-files` as the completion gate: it re-runs every hook over the whole repo — duplicating the commit run, forcing the slow always-on hooks (bats, nix, …) even for an unrelated diff, and **false-blocking** a clean change on a pre-existing violation in a file it never touched. Reserve `--all-files` for a deliberate full-repo sweep, not per-change validation.
- If the project has `flake.nix` (same exit-0 probe: `test -f flake.nix && echo yes || echo no`): `nix flake check` MUST pass. For machine-config validation use the build-only `nix build .#darwinConfigurations.<host>.system` (or `zn-self-build`) — `darwin-rebuild check` MUST NOT be used: on current nix-darwin it bails immediately with "system activation must now be run as root" and does NO build/eval (observed 2026, nix-darwin 26.05)
- IF no tests exist for changed code: create them
- NEVER claim code is complete without passing tests

> Observed 2026-07-30 (8-day census): 127 Bash timeouts across 69 sessions — mostly `git`
> fetch/clone on the monorepo, `nix` builds/checks, and test loops re-issued unchanged after the
> first timeout. 73 of the 127 were subagent calls, which is why **L-3** exists.

- **L-1** A command expected to outlive the 2m default (`nix build` / `nix flake check`,
  `go test ./...`, monorepo `git fetch|clone|push`, `prek`/`pre-commit run --all-files`) MUST set an
  explicit `timeout`, or run via `run_in_background` and be watched with Monitor.
- **L-2** After a timeout, the SAME command MUST NOT be re-issued unchanged; re-run it in the
  background or with a larger explicit timeout, and narrow it if possible.
- **L-3** A subagent brief that instructs a build, check, or full test run MUST state the timeout to
  use, or say to run it in the background.

#### Structured Data Files

MUST use `jq`/`yq`/`tq` for JSON/YAML/TOML manipulation over text-based editing (sed, awk, python).

#### Scratch / Payload File Writes

> Observed 2026-07-30: 125 of 134 Write errors in the 3-month census were "File has not been read
> yet", and the mechanism is unchanged in the 8-day re-measure — 9 of 11 precondition failures were
> regenerated payloads in the scratchpad (`commitmsg.txt`, `pr-body.md`, `*.jsonl` exports)
> overwritten at a path this or a sibling session already wrote. In one session the agent alternated
> between `commitmsg.txt` and `commit-msg.txt` rather than using a fresh name.

- **V-1** A regenerated payload (commit message, PR body, report, export) MUST go to a FRESH unique
  filename in the scratchpad (e.g. `pr-body.2.md`, `mktemp`-style suffix), not overwrite the
  previous revision. Renaming or re-spelling the same file is NOT a fresh name.
- **V-2** If overwriting an existing path is genuinely required, it MUST be Read first in this
  session, immediately before the Write. A ranged Read suffices — verified 2026-07-30, a `limit: 1`
  Read of a 4-line file satisfied the precondition — so the cost is one cheap call, not reading a
  large file in full.

#### Exit Codes

In ANY language, exit code 1 is the conventional general/catch-all error and MUST NOT be given a
specific branchable meaning. If an exit code must carry a specific meaning (so callers/scripts can
branch on it), it MUST be a distinct value >= 2, with 1 reserved for generic/unexpected errors.

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

### Beads Is The Issue Tracker For The Skills That Ask For One

> The `mattpocock-skills` plugin's skills (`/wayfinder`, `/triage`, `/to-tickets`, `/to-spec`)
> each read a per-repo "issue tracker" doc. They ship templates for GitHub, GitLab and local
> markdown only, and DEFAULT SILENTLY to local markdown when no tracker is provided — which
> would put planning state in `.scratch/` files, contradicting the beads-only rule. The beads
> binding is therefore written once and MUST be found from anywhere.

- **T-1** When a skill asks for this repo's "issue tracker", the answer is beads, and the
  binding is the **`wayfinder-beads` skill** — invoke it. It carries the `bd` operation
  mapping, `/wayfinder`'s "Wayfinding operations", and the triage label vocabulary. An agent
  MUST NOT fall back to local markdown, `.scratch/`, or GitHub Issues in a beads repo.
- **T-2** An agent MUST NOT run `/setup-matt-pocock-skills`. It would propose GitHub (a GitHub
  `git remote` is its default posture) and write its own tracker doc over the top. Changing
  trackers is an operator decision.
- **T-3** T-1 names a SKILL, never a path, and that is deliberate: the skill ships in this
  flake's nix-built marketplace, which `homeModules` registers automatically on every machine
  that imports it, so the binding needs no per-machine or per-repo file. An absolute path here
  would bind the rule to one checkout on one machine. MUST NOT reintroduce one.

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

### Superseding Rulings

> A bead body is what the autonomous queue HANDS to the next agent, so it is the one artifact a
> ruling MUST reach. Observed 2026-07-30 (`pg2-xx1y5`): an operator ruling ("do not commit the
> audit") was written into a doc header and two sibling beads but NOT into the RESUME bead, whose
> entire purpose was to instruct a later session. That bead was released to `/drain-beads` with its
> pre-ruling instruction intact; the drain session believed it and briefed a subagent to do the
> forbidden thing. The session that received the ruling never reached a release — a PEER released it
> — so a release-time duty would never have fired. The duty is at the moment the ruling lands.

- **S-1** When an operator ruling SUPERSEDES an instruction written in a BEAD BODY, that bead body
  MUST be amended in the SAME exchange as the ruling. Recording the ruling in adjacent artifacts — a
  doc header, a sibling bead, a session note — is NOT sufficient and MUST NOT be counted as
  propagation: the queue hands the next agent the BEAD, not the adjacent artifacts. A bead whose
  purpose is to instruct a later session (resume / next-session / handoff / follow-up) MUST be
  amended FIRST, not last.
- **S-2** The amendment MUST SUPERSEDE the instruction, not merely accompany it. Appending the
  ruling while the original instruction still reads as live leaves TWO live instructions and a later
  reader MAY act on either — the outcome S-1 exists to prevent. The superseded text MUST be
  rewritten or struck in the body, and the ruling MUST be recorded verbatim with its provenance (who
  ruled, when) so a later reader can tell an EXECUTED DECISION from an open question. That recorded
  ruling is also what F-9's `decided-against?` probe greps for.

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

  | Probe                  | Command                                                                                                                                                                                                                              | ⇒ STALE (premise moot / recorded value wrong)                                                                                                                                                                | ⇒ STILL LIVE (as recorded)                                                         |
  | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
  | **`landed?`**          | `git merge-base --is-ancestor <sha> main; echo $?`                                                                                                                                                                                   | `0` — the commit IS on main, so "maybe still unlanded" is already answered                                                                                                                                   | `1` — genuinely not on main                                                        |
  | **`pushed?`**          | `git fetch --quiet origin && git branch -r --contains <sha>`                                                                                                                                                                         | any output (e.g. `origin/main`) — already pushed, so "close once the push happens" is satisfied                                                                                                              | EMPTY output. Read the OUTPUT, not `$?` — the exit status is `0` either way        |
  | **`patch-identical?`** | `git cherry -v main <branch>`                                                                                                                                                                                                        | no output, or every line starts with `-` — an equivalent patch is already upstream under a DIFFERENT sha, so the branch is spent                                                                             | any line starting with `+` — that commit is genuinely not upstream                 |
  | **`path-exists?`**     | `git ls-tree -r --name-only main -- <path> [<path>…]`                                                                                                                                                                                | a path the plan edits is ABSENT from the output — but ABSENT is AMBIGUOUS (**F-9**): only once `decided-against?` finds NO ruling does it mean that module is GONE and a design prescribing edits to it void | every named path echoes back. Read the OUTPUT, not `$?`                            |
  | **`decided-against?`** | `bd list --desc-contains '<artifact>' --status all -n 0 --json`, then `rg -in 'operator ruled\|not to be committed\|superseded'` over those beads AND over the artifact's own untracked on-disk copy (full runnable form in **F-9**) | any recorded operator ruling forbidding the work — the ABSENCE IS the executed decision, so re-proposing the work is the defect                                                                              | no ruling in the artifact and none in ANY citing bead — absence means not-done-yet |
  | **`symbol-shape?`**    | `git grep -c -- '<symbol>' main -- <path>`                                                                                                                                                                                           | exit `1`, no output — the option/function/field the steps operate on no longer exists at that path                                                                                                           | exit `0` with `main:<path>:<n>` — still present                                    |
  | **`ticket-open?`**     | `pjira issue <KEY> \| jq -r '.status'`                                                                                                                                                                                               | `Closed` / `Done` / `Resolved` — the external work finished, so "continue `<KEY>`" is moot                                                                                                                   | anything else. `pjira`'s JSON is FLAT: `.status`, never `.fields.status`           |
  | **`sibling-open?`**    | `bd show <sib-id> --json \| jq -r '.data[0].status'`                                                                                                                                                                                 | `closed` — the bead this one waits on, duplicates, or defers to is done                                                                                                                                      | `open` / `in_progress` / `blocked`                                                 |
  | **`next-free-id?`**    | `printf '%04d\n' "$(( 10#$(git ls-tree -r --name-only main -- docs/adr \| rg -o '/(\d{4})-' -r '$1' \| sort -n \| tail -1) + 1 ))"`                                                                                                  | DIFFERS from the number the draft recorded — that id is TAKEN by someone else; renumber before landing                                                                                                       | equals the recorded number                                                         |

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
- **F-9** ABSENCE IS AMBIGUOUS. An ABSENT `path-exists?` reading means EITHER "not done yet" OR
  "RULED AGAINST — the absence IS the executed decision", and the two demand OPPOSITE actions.
  Before treating any absence as work to do — and specifically BEFORE briefing a subagent to
  create, restore, or commit the missing artifact — the agent MUST run the `decided-against?`
  probe and MUST record its output. Absent from `git` is NOT absent from DISK: an artifact
  deliberately left uncommitted still exists untracked, and its own header is the usual place
  the ruling is written (S-2).

  ```bash
  A='<artifact-name>'; R='operator ruled|not to be committed|do not commit|decided against|superseded'
  fd -HI "$A" "${PN_WORKSPACE_ROOT:-.}"                # EXISTS untracked? that alone is a signal
  rg -uu -in "$R" -g "*$A*" "${PN_WORKSPACE_ROOT:-.}"  # the artifact's OWN header usually carries it
  bd list --desc-contains "$A" --status all -n 0 --json |
    jq -r '.data[] | "== \(.id) ==\n\(.description)\n\(.notes)"' | rg -in "$R"
  ```

  `bd search` matches TITLE and ID only, so it MUST NOT be used here; `bd list --desc-contains`
  is the description search, and `--status all -n 0` is load-bearing — the bead holding the
  ruling is usually CLOSED and the default both excludes closed and caps at 50 rows (verified
  `pg2-xx1y5`: without `--status all`, 3 of the 6 citing beads are missed, the incident's own
  among them). A hit is DECISIVE: the work MUST NOT be re-proposed, and F-7's
  close-as-superseded applies. An UNRUN probe MUST NOT be read as "no ruling exists" (F-4).

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

> A local ff-merge makes work LANDED, not PUBLISHED, and the debt REGENERATES on every land — so it
> is computable state that no record can hold, and a standing bead for it is a defect (`pg2-5subz`
> nearly orphaned 11 unrelated commits; its replacement `pg2-dawg2` pushed 12, closed correctly, and
> the debt was back within a day). Unpushed commits are NOT in themselves a problem, so the
> OBLIGATION IS TO LOOK WHEN IT MATTERS — not to narrate the count at every session end.

- **U-1** Unpushed landing debt MUST be treated as DERIVED STATE and re-derived from git at the
  moment it matters. No bead, label, comment, handoff doc, or earlier reading is its handle: a
  reading is valid only for the instant it was taken (**F-1**) and MUST NOT be cached across a
  later land, a peer session, or a hand-off.
- **U-2** An agent MUST NOT create, maintain, or "restore" a standing push bead, a
  `push-carryover` bead, or a handoff-doc section whose PURPOSE is to remember that locally landed
  commits are unpushed. Such a record duplicates computable state, goes stale at the very next
  land, and can be closed while the condition it names is still true — that IS this defect. A bead
  for ONE push a person has already authorized as a discrete task is NOT this; making a bead the
  standing accounting for the aggregate debt IS.
- **U-3** In a `pn` workspace the probe is `pn workspace doctor`, run from anywhere inside the
  workspace — it already computes `origin/<branch>` vs local for EVERY repo, so an agent MUST reuse
  it rather than write another per-repo `git rev-list --count origin/main..main` loop. The debt is a
  `branch-synced` finding carrying `ahead N` with `N > 0`; `behind M` alone is NOT (that is
  un-pulled remote work), and a repo with no debt emits no section at all. The agent MUST read the
  `branch-synced` findings specifically — the trailer's error count also includes other checks. This
  workspace-wide AGGREGATE is NOT the **F-3** `pushed?` probe, which answers whether ONE named
  commit is on a remote; a single `pushed?` reading MUST NOT be generalized to "that repo is pushed".
- **U-4** The probe MUST be run READ-ONLY. An agent MUST NOT pass `--fix`: its `branch-synced` plan
  is `git merge --ff-only origin/<branch>` executed in the CANONICAL clone, which cannot publish an
  ahead-only divergence (so it does not clear the debt) and mutates the canonical clone, which
  **R-3** forbids.
- **U-5** Discharging the debt is OUTWARD-FACING and operator-authorized. An agent MUST NOT
  `git push`, `pn workspace push`, `pn workspace update`, or `pn workspace apply`, and MUST NOT
  invoke `/pn-workspace-sync` or `/pn-workspace-update`, on its own initiative to clear it.
  REPORTING is in scope; PUBLISHING is not. Trimming the reporting duty (**U-6**) does NOT relax
  this restraint.
- **U-6** REPORTING IS NOT MANDATORY, AND WHEN DUE IT MUST BE AT MOST ONE LINE. A count of
  unpublished commits is not a problem, so an agent MUST NOT give unpushed state its own heading,
  quote probe output verbatim, attribute commits to sessions, or spell out the remediation sequence,
  and MUST NOT run the U-3 probe merely to have something to report. A session that landed locally
  and is not blocked by that fact reports NOTHING about it. The one case that earns a line is a
  CONSEQUENCE for the work in hand: unpublished state BLOCKS it — e.g. a consumer flake pins these
  repos as `github:` inputs, so the change cannot take effect on apply until they are pushed and
  relocked. Then name the blockage and the repos in ONE line and stop; the operator asks for the
  probe output or the remediation path if they want it.

### Blocker Modeling: Dependency vs Human

> The two agent queues are keyed on ONE label. `/drain-beads` claims with
> `--exclude-label human`; `/unblock-human-beads` claims with `--label human`. So `human` does not
> mean "blocked" — it means **A PERSON IS THE BLOCKER**. Applied to "another issue must finish
> first" it does two wrong things at once: it hides the issue from the agent queue that would
> eventually work it, AND it puts a non-question in front of the operator, who is the one serial
> resource. Observed 2026-07-27: `pg2-l3vdz` was labeled `human` while needing no human input of
> its own — 6 of its 8 sub-issues had landed and it was waiting purely on two that needed
> decisions. Re-modeled as two `blocks` edges it left the human queue and stayed `status=open`,
> yet correctly absent from `bd ready`. Verified 2026-07-29: `bd show pg2-l3vdz` reports
> `status open` with `labels ["behavior-docs"]` and no `human`, `bd dep list pg2-l3vdz` echoes
> both blockers as `(open) via blocks`, and the id is absent from `bd ready`. The difference that
> matters is not tidiness. A label is a stored flag somebody must remember to remove, whereas
> readiness is DERIVED from the dependency graph — so the edge clears ITSELF when the last blocker
> closes, and the work flows back to the agent queue with no human touch at all.

- **D-1** Before applying `human`, an agent MUST classify each blocker by what would clear it:
  ANOTHER ISSUE that must finish first (⇒ a dependency), or a PERSON whose decision, input,
  approval, or out-of-band action is required (⇒ `human`). `human` MUST NOT be used as a generic
  "not workable right now" park, and MUST NOT be applied without that determination being made.
- **D-2** A blocker that is another issue MUST be modeled as a blocking dependency and MUST NOT be
  labeled `human`. The FIRST id is the BLOCKED issue and the SECOND is the BLOCKER — per
  `bd dep add --help`, "`issue-123` depends on (is blocked by) the specified issue":

  ```bash
  bd dep add <blocked-id> --blocked-by <blocker-id>
  bd dep list <blocked-id>   # CONFIRM: each blocker echoes back "(open) via blocks"
  ```

  The `--blocked-by` / `--depends-on` flag form SHOULD be preferred over the equivalent bare
  positional `bd dep add <blocked-id> <blocker-id>`, which reads identically whichever way round
  it is written and is therefore the form a reversal hides in. A reversed edge blocks the WRONG
  issue, silently. The `bd dep list` read-back is the cheap proof of direction and MUST be run.

- **D-3** The edge MUST be left at its default `blocks` type. `discovered-from`, `related`,
  `relates-to` and `supersedes` edges do NOT gate readiness — verified 2026-07-29: `pg2-dt9et`
  carries `discovered-from` to `pg2-l3vdz`, which is OPEN at P0, and `pg2-dt9et` was claimed as
  ready anyway. So the `--deps "discovered-from:<id>"` form — correct for recording PROVENANCE on
  `bd create` — MUST NOT be used to express "must finish first".
- **D-4** An agent MUST NOT pass `--no-cycle-check` when wiring a single edge. A cycle makes BOTH
  issues permanently unready, absent from every queue, which is this defect in its worst form. A
  bulk wiring that did skip the check MUST be followed by `bd dep cycles`.
- **D-5** ORDERING IS LOAD-BEARING: every dependency edge MUST be added BEFORE the claim is
  released. While the issue is `in_progress` and owned, `bd ready` excludes it, so the edges land
  in a window no peer can observe. Released first, the issue is momentarily `open`, unlabeled AND
  unblocked — long enough for a peer agent to claim work that is genuinely blocked. `bd dep add`
  and `bd update` are separate commands and cannot be made atomic, so the ORDER is the only guard
  (the same reasoning as **W-6**).
- **D-6** The release MUST set `--status open` and MUST clear the assignee in the SAME call
  (**B-2**, **B-3**):

  ```bash
  bd dep add <id> --blocked-by <blocker-id>   # once per blocker, FIRST
  bd update <id> --remove-label human --status open --assignee "" --actor "ID"
  ```

  `open` is correct and `blocked` is not: readiness is derived from the graph, so the issue is
  already out of `bd ready`, while a stored `blocked` status is a value nothing recomputes when the
  last blocker closes — it would strand the issue after the dependency resolved. The
  `--remove-label human` clause belongs in that same call whenever the issue already carried it.

- **D-7** A MIXED blocker — part issue, part person — MUST get BOTH treatments; neither half may be
  dropped, and the `human` label MUST sit on the issue that actually HOLDS the question. The agent
  MUST record the consequence, because it is not obvious: `bd ready` excludes blocked issues, so an
  issue carrying `human` AND an open blocker is absent from BOTH queues until the blockers clear,
  and only then resurfaces in `bd ready --label human`. Verified 2026-07-29 — `pg2-4dz88`,
  `pg2-wr6lm.9`, `pg2-qhhil` and `pg2-r1f1j.9` each carry `human` with one open blocker, and none
  of the four appears in `bd ready --label human`. That is CORRECT when the question is only
  answerable after the blocker lands. When the question is answerable NOW and independent of the
  blocker, the agent MUST NOT bury it behind the edge: it MUST file the question as its own
  `human` issue with no blockers and make this issue depend on THAT, so the operator sees the
  question immediately while this issue carries no label of its own.
- **D-8** THE REVERSE CONVERSION IS MANDATORY TOO, AND IT NEEDS NO PERSON. An issue already labeled
  `human` whose only live blockers are other issues is MISLABELED, not blocked on a person.
  Whoever finds it MUST convert the label into dependencies per **D-2**/**D-5**/**D-6**, and MUST
  NOT engage a person to authorize that conversion: there is no question to ask, so a prompt spends
  the one serial resource to discover there was never one. A blocker that probes `closed`
  (**F-3** `sibling-open?`) is not a blocker at all and MUST NOT be given an edge — that issue's
  label reason simply died. A dependency MUST NOT be substituted by a DEFER window either: a defer
  is a TIMER that expires whether or not the blocker cleared, while an edge IS the state.
- **D-9** A PARENT MUST NOT BE HELD OUT OF THE QUEUE WHILE ITS CHILDREN ARE STILL TO BE WORKED.
  `bd` propagates BOTH `blocked` AND `deferred` DOWN parent-child, so a parent in either state
  hides its ENTIRE subtree from `bd ready` — the children read `status=open`, unassigned,
  undeferred, with no blocking edge of their own, and are still absent. Two consequences, and
  neither is discoverable from the children:
  - An agent MUST NOT wire a parent `--blocked-by` its OWN child. That shape is a DEADLOCK, not a
    dependency: the parent is blocked because the child is open, and the child is hidden because
    the parent is blocked, so neither side can ever be worked and nothing external clears it. This
    is the one place **D-2**'s "model it as a blocking dependency" MUST NOT be applied — a
    container parent's relationship to its children is ALREADY expressed by parent-child.
  - An agent MUST NOT `--defer` a parent whose children it still wants worked. A defer looks like
    bookkeeping on one issue and silently parks the whole subtree.
    A container parent with no deliverable of its own therefore has exactly two honest states: OPEN
    (and re-triaged whenever it surfaces), or CLOSED. `bd close` refuses it with "cannot close epic
    <id>: N open child issue(s)"; `--force` is the documented override and is NON-DESTRUCTIVE —
    verified 2026-08-19 that after a forced close the children stay `open` and READY and
    `bd list --parent <id> --status all -n 0` still reconstructs the roll-up.
    Observed 2026-08-19 (`tc-i1t9`, and the ruling that filed it): applying the
    convert-to-dependency remedy to three bb container epics removed 4 ready decision beads from
    every queue, and the SAME shape pre-existed under `tc-gh4j`/`tc-airc`, leaving 18 open bb leaves
    permanently unworkable — which is why `bd ready --exclude-label human --label bb` read EMPTY
    while the decomposition had 18 open leaves. Removing the parent→child edges restored them
    immediately. Diagnosing this MUST be done with the claim RELEASED: `bd ready` excludes
    `in_progress` on its own, so the test is vacuous while the parent is claimed.

### General Guidelines

- Before recommending paid/licensed software, confirm the cost with the user.
- When telling the user which file to view/open (design docs, specs, code), ALWAYS give the full
  absolute path, never a repo-relative one — many concurrent worktrees/workforests run across
  sessions, so a relative path is ambiguous about which checkout is meant.

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
- **R-9 (integration entry point)** To integrate completed work, the agent MUST invoke the Skill tool with the plugin-qualified id `integrate-branch:integrate-branch` (handlers: `integrate-branch:ff-merge-to-main`, `integrate-branch:pull-request`; session close-out is `session-wrapup:wrap-up-session`). Qualified ids are the form the Skill tool documents for plugin skills, and they are unambiguous where a bare name is not: a bare name can resolve to a different plugin's skill silently, whereas a stale qualified id fails loudly as `Unknown skill: <id>`. Bare names DO currently resolve — verified 2026-07-30: 7 bare `integrate-branch` invocations succeeded among 199 Skill calls over 8 days — so this is a SPECIFICITY requirement, NOT a fix for a live failure, and MUST NOT be cited as evidence of one. The agent MUST NOT use `superpowers:finishing-a-development-branch` (plain non-ff merge, no rebase).

### Prohibited Actions

#### System Commands

- **CRITICAL**: NEVER run system activation commands (e.g., `darwin-rebuild switch`) without explicit user request — these are user-only commands
- **CRITICAL**: NEVER use `sudo`
- When building/validating nix changes without activation, use a build-only command

#### Version Control

- **ZR monorepo ONLY** (ZR-Private/ziprecruiter): include the Jira issue as `Refs: TICKET-ID` on the line immediately after the subject (before the body). Extract the ticket ID from the branch name (format: `username.TICKET-ID.description`). A valid ticket ID matches `[A-Z]+-\d+` (e.g., `FINDEV-9208`, `CI-1494`). If the branch contains `NO-JIRA`, `NOJIRA`, or any variation instead of a real ticket ID, omit the `Refs:` line entirely. In personal/nix repos (the phillipg_mbp workspace and similar), the ticket-branch format does NOT apply: use simple branch names (e.g. `fix-foo`) and never add a `Refs:` line.
- **CRITICAL**: NEVER use `--no-verify` (or `-n`) on git commands without explicit user approval
- IF git hooks report violations: MUST fix the violations rather than bypassing hooks
- Agent-authored GitHub PR comments/reviews (ZR repos) MUST include 🤖 in the body — a hook rejects them otherwise (12 rejected-and-retried comment bodies in the 3-month census; 1 in the 8 days to 2026-07-30)

#### Waiting / Polling

> Observed 2026-07-30 (8-day census): 26 foreground-`sleep` blocks across 26 DISTINCT sessions —
> exactly one each, so the reflex is re-learned from scratch every time. 21 of 26 were subagents. 12
> of 26 were `sleep N` followed by `tail`/`cat`/`wc -c` on a background job's scratchpad log, which
> is the exact case Monitor exists for. The Bash tool description already states this prohibition and
> is demonstrably not sufficient on its own.

- **CRITICAL**: NEVER wait by foreground `sleep` — it is policy-blocked, so the call is a guaranteed
  wasted round trip.
- To wait for a background job's output to change or a file to appear: `run_in_background`, then
  Monitor with an until-loop. MUST NOT poll it with `sleep` plus `tail`/`cat`/`wc`.
- To wait on external state (a PR merging, a CI run finishing): Monitor with an until-loop, or a
  single check at a delay matched to how fast that state actually changes — never a `sleep`-then-check
  pair.

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
