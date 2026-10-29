# Agent Rules: Rationale, Census Data and Provenance

This document holds the "why" for the always-on user-global rules in
`home/programs/agent-rules/pgii-agent-rules.md` (deployed to `~/.claude/CLAUDE.md`). It is NOT
loaded at runtime. The runtime file carries only the normative one-liners, keyed by the same rule
IDs used below. Delivery design: `docs/superpowers/specs/2026-06-25-agent-rules-delivery-design.md`.

## Where rules live (tc-ql0o staging)

- Stage C (tc-ql0o.3, operator Decisions 1-3, 2026-08-25/26): the `B-*`, `D-*`, `P-*`, `F-*` and
  `W-*` packs moved into the `beads-lifecycle` skill. Each keys on an observable trigger (a `bd`
  verb, a park/release/accept action, a label mutation) that a MUST-invoke tripwire in the core can
  gate on, so the full text no longer rides in every session. `S-1`/`S-2` and `T-1`..`T-3` stay in
  the core: they have no such trigger (a conversation-time ruling and a bootstrap-paradox binding,
  respectively).
- Stage D (tc-ql0o.4, 2026-08-26): exit-code conventions, unit-test isolation and structured-data
  tooling moved to the `code-file-standards` path-rule (each is scoped to a file type, so it rides
  in only when a matching file is read). `R-7`/`R-8` moved to the `integrate-branch` skill (both key
  on the landing moment, which `R-9` forces every integration through; `ff-merge-to-main`'s FF-0a
  and FF-3 implement R-8 and R-7 in executable form). `U-1`..`U-4`/`U-6` moved to the
  `session-wrapup:wrap-up-session` skill's `references/unpushed-landing-debt.md`.
- Instruction-trim change (this document's origin): the runtime file was tightened to terse
  one-liners; census/provenance blockquotes moved here; `BF-3`/`BF-4` moved to the
  `beads-lifecycle` skill; the `pg-hooks` state reference moved to the `nix-how-to` path-rule; the
  per-repo CI list moved here (below).

## Mistake Acknowledgment Marker (M-1..M-3)

Purpose: make agent self-corrections MECHANICALLY COUNTABLE so their rate can be tracked over time.
It is a WORDING convention only. Adopted 2026-07-30; it generates data FORWARD ONLY and cannot be
backfilled, which is why it landed ahead of the tooling that will consume it.

- M-2: if M-1 would increase acknowledgment frequency, M-1 is being misapplied.
- M-3: provenance (self-caught vs user-caught) is derived from transcript structure (whether the
  preceding turn was a typed user prompt), so stating it is redundant.

## Absolute-Path Provenance (A-1..A-3)

Observed 2026-07-30 (8-day census, 924 transcripts): 104 of 152 failed Reads named a root that does
not exist on this machine (99 `/home/...`, 4 `/mnt/user-data/...`, 1 `/repo/...`) across 86 distinct
sessions, worst single session 3, and 100% in the main loop rather than subagents. In the traced
cases the task gave repo-RELATIVE paths and the agent, required to use absolute paths, FABRICATED a
root instead of resolving against the session cwd. The failure text names the real cwd, so each one
is a round trip spent asking for something the harness had already answered.

The A-1 known-absent-roots sentence is machine-class-specific and is composed in
`home/programs/agent-rules/default.nix` from `phillipgreenii.programs.claude-code.knownAbsentRoots`.
A-3: a brief that lists relative paths without a root causes exactly this defect.

## Validation gates

- Both gate bullets (hooks exist; `flake.nix` exists) stay in the core unconditionally (tc-ql0o
  Stage D, 2026-08-26): they trigger on a repo PROPERTY, not on reading a `.nix` file. A Go-only edit
  in a flake repo never reads one (the `pg2-3nb2t` class), so a file-glob path-rule cannot carry the
  obligation, only the HOW-TO detail once you are already working with `.nix`/`flake.nix` files.
- Bare `ls` as a hook-presence probe produced 19 failed tool calls in the 8 days to 2026-07-30.
- Full `nix flake check` ruling (operator, Phillip, 2026-10-01): not a per-change or land-time gate;
  it overrides any older rule telling an agent to run a full flake check at land or before
  committing. The gates are the commit's own hook run and, at land, `ff-merge-to-main`'s FF-1b
  `pg-hooks run pre-land` over the branch diff, plus FF-1c's eval-only `pre-land-eval` in a clone that
  opted in (operator ruling, Phillip, 2026-10-23: a separate gate, not a full flake check).
- Per-repo CI posture: repos with cloud CI (`phillipgreenii-nix-support-apps`,
  `phillipgreenii-nix-personal`) keep CI as the whole-repo gate. Accepted interim risk:
  `phillipgreenii-nix-agent-support` and `phillipg-nix-ziprecruiter` have no CI, so their only
  automatic test runners are the commit-time `run-unit-tests` hook (`pg-test-runner`, touched
  projects) plus FF-1b's `pg-hooks run pre-land` over the branch diff; if that lets problems
  through, that is the signal to bring CI back.
- V-3 (operator ruling, Phillip, 2026-10-08, verbatim: "the checks and verifications for
  deterministic code should be in a test"). Provenance: every pg-wi-flow test mocked `bd`, so two real
  bugs (`bd` refusing a bare `--assignee` reassign of another actor's live claim, and a `.data // .`
  jq idiom that errors on the bare array `bd` returns when `BD_JSON_ENVELOPE` is unset) shipped and
  only surfaced in a live `/drain` run, and the fix was then re-verified by hand in a throwaway
  database to close "verify after apply" beads. The landed guard is
  `packages/pg-wi-flow/pg-wi-flow/tests/test-pg-wi-flow-integration.bats` (real `bd`, throwaway
  embedded Dolt database).
- V-4 (operator ruling, Phillip, 2026-10-25, verbatim: "unit tests should run with minimual
  setup/fixtures. the expectation is a single test should be millisconds, packages of tets should be
  measured in seconds. unit tests for a full app, could take a minute or two, there isn't a hard
  limit on that. but unit tests should be very fast and can be run in any order"; and "load tests
  and integration tests are not unit tests and should not run as unit tests and should not be used
  to block commits or lands. if there is a concern, then a bead gated on the code being deployed
  should be created and it can run the integartion/load tests based upon main"). Provenance: the
  pg-connector-github `internal/loadtest` package (150 simulated PRs over 19 minutes of fake time,
  run twice plus three mutation runs) sat inside `checks.<system>.pg-connector-go-tests`, so every
  build and land paid for it; on 2026-10-25 a build ran the package 1800.891s and died with
  `panic: test timed out after 30m0s` (bead `pg2-7r19vr` moves it out, `pg2-4u5i2h` diagnoses and
  speeds it up). Its `-timeout 30m` flag in `flake.nix` was the earlier symptom: the fix then was to
  raise the ceiling instead of moving the test. The "own check" half is `phillipg-nix-repo-base`
  ADR 0024 (a tagged-out suite that nothing runs rotted in `modules/pn`, bead `pg2-nuacd`); the
  deploy-gated run against `main` is what keeps an on-demand suite from becoming that again. Existing
  rules said nothing on test cost or kind: ADR 0037 limits which commands run tests and HK-2 keeps
  `nix` out of hooks, neither classifies a test.
- V-5 (operator rulings, Phillip: 2026-10-20 on `pg2-6oop0.8`, verbatim: "typical development it
  not supposed to do a full mutant run because it takes so long"; 2026-10-22, verbatim: "unless the story is about mutation testing
  itself, none of the standard planned work should rely on running mutate tests"; 2026-10-26,
  verbatim: "typical development flows must not invoke mutation tests. mutation tests are something
  done outside of standard implementation flows ... each use of mutation tests should be intentional
  outside of implementation work. it is used to generate areas which need to be improved"; and
  the same day, on closing the gaps an analysis found, verbatim: "i don't think we need to re-run
  the mutation tests at all. i think what the mutation had uncovered when it ran was the problem.
  the problem is fixed when a test is written which covers a missed path. as long as the acceptance
  criteria includes the path and test(s) which should exist, i think that is sufficent to fixing
  the problem. a mutation test would run on this app/package later and it shouldn't find the same
  issue twice"). Provenance: the `go-test-gaps` skill description told agents to use it "after
  implementing a Go feature and deciding what to test", so it fired in ordinary work; and its
  "Use 2" re-ran the tool to confirm a closed gap, which would have sent every drained triage bead
  back into an hours-long run (two independent reviews of the first version of this rule found
  that), so the confirm-by-re-run section was removed. Seven documents carried "run
  `pg-go-mutate`" rows that `plan-decompose` copies into packet acceptance criteria and were
  struck 2026-10-26: six design specs and plans in this repo plus one in `phillipg-nix-ziprecruiter`.
  Earlier strikes: a bead acceptance criterion on pg2-z5fax.4 (2026-10-22) and the PR-lane and
  connectord-hoist specs (2026-10-22). A 2026-10-20 mutation run spawned an unbounded process tree
  (`nix-support-apps` observability README). A bare prohibition has no observable in-session
  trigger, so it stays always-on rather than moving into the skill (tripwire pattern). The
  hand-applied negative control is excluded because `x/pprofserver/mutation-check.sh` (in the `x`
  repo) and the dashboard-check mutant self-tests are ordinary fast tests; the tooling-source
  exception covers `nix-overlay`'s `gomu-engine-contract-tests`, which runs the real tool against a
  generated toy project. Enforcement is textual only: the tool-approver allowlists `pg-go-mutate`
  as no more permissive than `go`, so nothing blocks a run mechanically. A third review pass
  found the tooling exception worded three ways (some wordings allowed a run on a real package,
  which would have let a gap-closing bead for a package inside the tool revive the re-run), so it
  now reads the same everywhere: synthetic fixture only, never a workspace package, never to
  confirm a closed gap. That pass also removed the skill's `--workers` claims (the tool ignores
  the flag and forces one worker) and its "single-file targets are rejected" line (a `.go` file is
  accepted). A fourth review pass (2026-10-26) found that the sweep files no triage bead at all
  (`FileTriageBead` has no caller), so the "analysis amends the sweep's tally bead" text described
  nothing. The operator then ruled the intended flow (recorded verbatim on `pg2-tkm9g0`): when a
  package's run completes, one triage bead carries the results; an agent analyzes them without
  re-running the tool, files `discovered-from` fix beads, and closes the triage bead listing them or
  saying why none are needed. The same pass settled one framing for tooling self-tests: a run of the
  real tool against a synthetic fixture is not mutation testing, which is what keeps nix-overlay's
  `gomu-engine-contract-tests` legitimate as a check.

## Timeouts (L-1..L-3) and host load (L-4)

Observed 2026-07-30 (8-day census): 127 Bash timeouts across 69 sessions, mostly `git`
fetch/clone on the monorepo, `nix` builds/checks, and test loops re-issued unchanged after the
first timeout. 73 of the 127 were subagent calls, which is why L-3 exists. The `pg-nix-log-wrapped`
requirement in L-3 comes from the always-on workspace rule (epic pg2-kqrrs); a subagent does not
reliably inherit that rule.

L-4 (bead `pg2-klusl`, from `pg2-r9ly8`'s 2026-10-07 attribution): the host sat at 1m load 50-150
on 11 cores because 3-5 `go test -race` trees ran at once, all with go's default `-p` (every core)
at normal priority. `pg-test-runner` bounds only its own runs (2 host-wide slots, `nice -n 10`;
repo-base commit 77da2451); direct agent `go test` bypasses it, and so did nix `*-go-tests` builds
until this repo's overlay wrapped `mkGoTest` with the same two bounds (`-p=4`, `nice -n 10`). The
values mirror those: 10 matches `niceLevel`; 4 is a bit over a third of the cores, so one run still
parallelises. L-4 is deliberately NOT a claim budget or admission gate: the drain loop is endless by
design (operator ruling), so the lever is making each run cheaper to the host, not running fewer.

## Scratch / Payload File Writes (V-1, V-2)

Observed 2026-07-30: 125 of 134 Write errors in the 3-month census were "File has not been read
yet", and the mechanism is unchanged in the 8-day re-measure: 9 of 11 precondition failures were
regenerated payloads in the scratchpad (`commitmsg.txt`, `pr-body.md`, `*.jsonl` exports)
overwritten at a path this or a sibling session already wrote. In one session the agent alternated
between `commitmsg.txt` and `commit-msg.txt` rather than using a fresh name. V-2: verified
2026-07-30, a `limit: 1` Read of a 4-line file satisfied the precondition, so the cost is one cheap
call, not reading a large file in full.

## Beads lifecycle stubs (B-1/B-2, B-5, F-1, F-9)

- B-1/B-2 essence stays always-on regardless of skill invocation, since a tool-restricted subagent
  with Bash but no Skill tool can still violate it.
- B-5 (same reason): a claim in the operator's name looks deliberate, is never released, and
  strands the bead so no agent picks it up. Machine `bd` wrappers MAY refuse such a claim outright.
  A bead merely CREATED in the operator's name is acceptable; the claim is the concern.

## Issue tracker binding (T-1..T-3)

The `mattpocock-skills` plugin's skills (`/wayfinder`, `/triage`, `/to-tickets`, `/to-spec`) each
read a per-repo "issue tracker" doc. They ship templates for GitHub, GitLab and local markdown
only, and DEFAULT SILENTLY to local markdown when no tracker is provided, which would put planning
state in `.scratch/` files, contradicting the beads-only rule. The beads binding is therefore
written once and MUST be found from anywhere.

- T-1: the `wayfinder-beads` skill carries the `bd` operation mapping, `/wayfinder`'s "Wayfinding
  operations", and the triage label vocabulary.
- T-2: `/setup-matt-pocock-skills` would propose GitHub (a GitHub `git remote` is its default
  posture) and write its own tracker doc over the top.
- T-3: the skill ships in this flake's nix-built marketplace, which `homeModules` registers
  automatically on every machine that imports it, so the binding needs no per-machine or per-repo
  file. An absolute path would bind the rule to one checkout on one machine.

## Filing beads in the right tracker (BF-1..BF-4)

`bd` resolves its database from the CURRENT WORKING DIRECTORY, so a session whose cwd sits in one
repo silently files tooling/personal-workspace bugs into THAT repo's tracker. Observed 2026-09-29
(bead `pg2-84y3y`): a review of one tracker migrated a dozen misfiled beads to the workspace
tracker, and a worker whose correct tracker was unreachable fell back to the wrong one. Each repo's
own `CLAUDE.md` "Beads Labels" section, and the workspace-level `CLAUDE.md` repo/label lookup table
(machine-local), say which repo owns which tracker; the rule does not restate them. BF-3/BF-4 full
text now lives in the `beads-lifecycle` skill.

## Superseding Rulings (S-1, S-2)

A bead body is what the autonomous queue HANDS to the next agent, so it is the one artifact a
ruling MUST reach. Observed 2026-07-30 (`pg2-xx1y5`): an operator ruling ("do not commit the
audit") was written into a doc header and two sibling beads but NOT into the RESUME bead, whose
entire purpose was to instruct a later session. That bead was released to `/drain-beads` with its
pre-ruling instruction intact; the drain session believed it and briefed a subagent to do the
forbidden thing. The session that received the ruling never reached a release (a PEER released
it), so a release-time duty would never have fired. The duty is at the moment the ruling lands.

S-1: the queue hands the next agent the BEAD, not the adjacent artifacts. S-2: appending the ruling
while the original instruction still reads as live leaves TWO live instructions and a later reader
MAY act on either; the recorded verbatim ruling with provenance (who ruled, when) is also what the
`beads-lifecycle` skill's F-9 `decided-against?` probe greps for, and lets a reader tell an EXECUTED
DECISION from an open question.

## Unpushed Landing Debt (U-5)

A local ff-merge makes work LANDED, not PUBLISHED, and the debt REGENERATES on every land, so it is
computable state that no record can hold, and a standing bead for it is a defect (`pg2-5subz`
nearly orphaned 11 unrelated commits; its replacement `pg2-dawg2` pushed 12, closed correctly, and
the debt was back within a day). Unpushed commits are NOT in themselves a problem, so the
OBLIGATION IS TO LOOK WHEN IT MATTERS, not to narrate the count at every session end. The full
derivation / never-standing-bead / read-only-probe / one-line-reporting contract (U-1..U-4, U-6)
lives in `session-wrapup:wrap-up-session`'s `references/unpushed-landing-debt.md`. U-5 stays in the
core: it is a bare prohibition that applies at ANY moment, not only at session close-out, so it
has no session-close-scoped trigger a skill could gate on (Design P1). Trimming the reporting duty
(U-6) does NOT relax this restraint. History: ADR 0108 (Accepted 2026-10-27, `pg2-ukat2`) named the
`pg-repo-runner` push step as an exception, because a daemon is not an agent session. A ruling of
Phillip, 2026-10-28 (`pg2-pn6l7b`), let an agent session push, switch branches and manage its
worktrees. A final ruling of 2026-10-29 (`pg2-kwbhc2`, verbatim "we should remove the line from the
CLAUDE. pushing is ok." and "git push and pn workspace push, not pn workspace update or pn
workspace apply") replaced U-5 with its current scope: an agent MUST NOT run `pn workspace update`
or `pn workspace apply`, or invoke `/pn-workspace-update`, on its own initiative; `git push` and
`pn workspace push` are allowed. `/pn-workspace-sync` was NOT ruled on, so its prior prohibition is
kept unchanged. Whether the runner's own push gate in ADR 0108 stays is likewise undecided and
needs its own ruling; U-5 still forbids an agent from enabling that step or widening its scope.

## Local Containers (C-1..C-4)

Origin: docket `pg2-a01hh` (local container monitoring and reclaim). Containers in ANY state pin
their images and volumes, so a leaked or foreign container silently blocks disk reclaim, and an
agent that removes what it did not start can destroy another session's work or the operator's data
(volumes hold data; images cost a network pull to regenerate). The four rules therefore split on
OWNERSHIP: `C-1` makes the starting agent responsible for cleanup (`--rm`, and a Colima VM it
started is returned to the state it was found in), `C-2` makes ownership attributable through
`bead=` and `owner=` labels so a later reader can tell whose container it is, `C-3` is the bare
prohibition on touching anything not started or created by the agent (including the `prune`
verbs) and confines `pg-disk-reclaimer reclaim` to a dry run with explicit item ids, leaving any
`--apply` at level 4 or 5 to the operator, and `C-4` is the one carve-out: a bead that declares a
container cleanup step lets its agent remove containers labeled with that bead id. The rules have
no executable trigger a skill could gate on (they apply at any `docker`/`colima` invocation), so
they stay in the always-on core, one line each. The mechanical CETA guard for `C-3` is a separate
direct bead (D3), and the workspace `disk-cleanup` skill edit is D1.

## Integration discipline (R-1..R-9)

R-9: qualified ids are the form the Skill tool documents for plugin skills, and they are
unambiguous where a bare name is not (a bare name can resolve to a different plugin's skill
silently, whereas a stale qualified id fails loudly as `Unknown skill: <id>`). Bare names DO
currently resolve (verified 2026-07-30: 7 bare `integrate-branch` invocations succeeded among 199
Skill calls over 8 days), so this is a SPECIFICITY requirement, NOT a fix for a live failure, and
MUST NOT be cited as evidence of one.

## Version control

- The ZR monorepo `Refs:` rule and the agent-authored PR comment marker apply only in ZR repos. The
  rule was previously always-on; no ZR-specific rules source exists in this workspace (no
  `phillipg-nix-ziprecruiter` checkout), so it remains a two-line item in the core.
- 12 agent-authored PR comment bodies were rejected-and-retried by the hook in the 3-month census;
  1 in the 8 days to 2026-07-30.

## Waiting / Polling

Observed 2026-07-30 (8-day census): 26 foreground-`sleep` blocks across 26 DISTINCT sessions
(exactly one each, so the reflex is re-learned from scratch every time). 21 of 26 were subagents.
12 of 26 were `sleep N` followed by `tail`/`cat`/`wc -c` on a background job's scratchpad log, which
is the exact case Monitor exists for. The Bash tool description already states this prohibition and
is demonstrably not sufficient on its own.

## Subagent Fork Dispatch (FK-1, FK-2)

Observed in the improvement retro for 2026-08-17 to 08-31 (bead `pg2-yeh5f`): "Fork is not
available inside a forked worker" fired 47 times across 6 sessions (worst 18), 0 main-loop / 47
subagent. The rejecting condition is being ALREADY a dispatched subagent, not being specifically a
`fork`-type one: `general-purpose` workers hit the same rejection when they themselves tried
`subagent_type: "fork"`. 77 retry chains (70 FAILED retries) show workers re-issuing the identical
rejected call instead of adapting.

## Beads / Dolt no-autostart

See ADR 0032 (`docs/adr/0032-beads-dolt-no-autostart.md`). The runtime text keeps three bullets:
no casual `bd dolt start`, no auto-starting IDE extensions (e.g. `planet57.vscode-beads`, a classic
rogue-server cause: it polls `bd dolt status` and auto-runs `bd dolt start`), and daemons/timers
using the machine's wrapped `bd` (the overlay wrapper exporting `BEADS_DOLT_AUTO_START=0`).
