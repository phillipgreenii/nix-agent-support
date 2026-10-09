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
  `pg-hooks run pre-land` over the branch diff.
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

## Timeouts (L-1..L-3)

Observed 2026-07-30 (8-day census): 127 Bash timeouts across 69 sessions, mostly `git`
fetch/clone on the monorepo, `nix` builds/checks, and test loops re-issued unchanged after the
first timeout. 73 of the 127 were subagent calls, which is why L-3 exists. The `pg-nix-log-wrapped`
requirement in L-3 comes from the always-on workspace rule (epic pg2-kqrrs); a subagent does not
reliably inherit that rule.

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
core: it is a bare prohibition against ANY push/apply/update at ANY moment, not only at session
close-out, so it has no session-close-scoped trigger a skill could gate on (Design P1). Trimming
the reporting duty (U-6) does NOT relax this restraint.

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
