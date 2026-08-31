---
description: Weekly-ish improvement retro over recent sessions — ranks fixes to your skills/rules/hooks/scripts, asks you to accept/reject each by number, then files beads and records the rulings. Long blocking interactive run (~$10 in classifier calls; it stops and waits for your rulings); wraps tool-error-waste-review (queries the pg-ccaudit index, never raw JSONL)
argument-hint: "[--since YYYY-MM-DD] [--until YYYY-MM-DD] [--preview] — empty = since last retro"
---

# Improvement retro

Retro lens: rank by what should CHANGE (rule / skill / hook / script / harness), not
merely by what failed. The `tool-error-waste-review` skill owns the census method and
its both-halves rule stands — command failures and never-failed mistakes, ONE ranked
list; this command MUST NOT shortcut or re-derive the skill's method. What this
command adds is the lifecycle around the census: window continuity, prior rulings,
an operator decision round, and durable recording.

## Step 0 — home tracker, resume check, window, estimate, preflight

**Home tracker.** Every lifecycle `bd` call in this command (run records, rulings)
MUST target the pg2 tracker via `bd -C /Users/phillipg/phillipg_mbp …` — cwd silently
decides which tracker a bead lands in, and continuity breaks if run records scatter.
Item beads for accepted fixes MAY live in other trackers; run records MUST NOT. The
`improvement-retro` label is RESERVED for run records: item beads and carry beads
MUST NOT carry it, and any bead filed with `--parent <run-record>` MUST pass
`--no-inherit-labels`.

**Resume.** Check for an open run record:
`bd -C /Users/phillipg/phillipg_mbp list --label improvement-retro --sort created -n 1 --json`
(the default filter excludes closed rows only; this query only establishes the id —
read checkpoints from `bd -C /Users/phillipg/phillipg_mbp show <id> --json`). If one
exists and its latest checkpoint came from THIS effort's prior attempt, this run is a
resume: reuse its requested window verbatim (MUST NOT recompute), skip the phases
whose checkpointed tables are already in its notes, and count cost cumulatively
across attempts. A resume SKIPS the Window and Estimate/gate/create steps below —
the window is the record's, and the record already exists: re-derive only the
remaining phases' estimate, check it against the 1.5× ceiling using the checkpoints'
cumulative cost, print the PREFLIGHT screen with `provenance: resume <bead-id>` and
the existing run-record id, and say so on line 1 ("Resuming <bead-id> from phase
<n>"). An interrupted run is therefore recoverable by re-running with no arguments.
An open record with NO checkpoints died in Step 0: if the session id in its body is
this session's own, adopt it (reuse its requested window, create no new record);
otherwise STOP and report it. If the open record's latest checkpoint or activity is
from a DIFFERENT session that is still recent, STOP and report the conflict — MUST
NOT resume it or open a second record. A record whose latest activity is from a
different session and is NOT recent is abandoned: close it
(`bd -C /Users/phillipg/phillipg_mbp close <id> --reason 'abandoned, consumed no window'`)
before proceeding.

**Window.** Normalize `$ARGUMENTS` to `--since YYYY-MM-DD --until YYYY-MM-DD`
(`--until` exclusive, matching the canned queries) before invoking anything. Reject
`--since >= --until`. If empty: read the most recent CLOSED run record —
`bd -C /Users/phillipg/phillipg_mbp list --label improvement-retro --status closed --sort closed -n 1 --json`
(`--sort closed` is newest-first already; adding `--reverse` returns the OLDEST
record and silently re-censuses all history; plain `bd list` excludes closed rows no
matter the `-n`; `bd search` ranks by title/ID and hides closed rows unless
`--status all` — the wrong instrument for "the single most recent closed record") —
and use its `next_since` field as `--since`. An explicit `--since` always overrides
the derived one. If no closed run record exists (first run), default to the past 14
days and say so in the preflight; MUST NOT stop to ask. Clamp `--until` to the DATE
of the high-water timestamp reported by `pg-ccaudit status` (`--until` is exclusive,
so the partially-indexed day is excluded, not consumed) and print the clamp in the
provenance line — days the index has not reached are NOT consumed and roll into the
next retro. If `pg-ccaudit status` reports index-not-found, STOP: no run bead,
nothing consumed.

**Estimate, then the gate, then the run record.** Run
`pg-ccaudit candidates --since <start> --until <end>` (structural only, no model
calls) for the candidate count. Per-call cost comes from the previous run record's
`total $ ÷ calls`; on first run assume $0.02/call and label it an assumption.
Estimated calls = `min(candidate count, --max <N>)` — show the min and the
multiplication. MUST STOP for approval if the estimate exceeds $15 or the window
exceeds 30 days; if the operator declines, create nothing — no bead, no window
consumed. Once clear, create and claim the run record in one chained Bash call
(`bd create` has no `--status` flag, so it is two `bd` calls):
`bd -C /Users/phillipg/phillipg_mbp create "Improvement retro <start> → <end>" --labels improvement-retro …`
then `bd -C /Users/phillipg/phillipg_mbp update <id> --claim`, body holding the
requested window AND this session's id (the adopt-or-STOP test above reads it).
Then print ONE screen and proceed without asking:

```
PREFLIGHT — window: <start> → <end> (<N> days; provenance: args | <bead-id> | first-run default; --until clamped to index high-water <date> if applicable)
index: <pg-ccaudit status coverage + staleness line, verbatim>
run record: <bead-id>
plan: 3 census subagents; classifier bounded at --max <N>
estimate: min(<candidates>, <--max>) = <N> calls × $<per-call> ≈ $<X>
```

**`--preview`.** Run only `pg-ccaudit status`, the structural candidate counts, and
the closed-record lookup for the per-call cost (or the first-run $0.02 assumption) —
no classifier, no subagents, and NO bead: a preview consumes no window, so it records
nothing. Print "N correction candidates, M repetition candidates since <date>; est.
$<X> to classify (same estimate arithmetic as above)" and stop. This is the cheap
"is there enough new material?" probe.

## Census — delegated

Invoke the `tool-error-waste-review` skill; follow its method and step order. Run its
heavy dimensions in read-only subagents — each is briefed to load the
`tool-error-waste-review` skill itself and work ONLY its assigned sections, and to
return in one turn with a stated row cap, no waiting across turns, no writes, and no
raw-transcript access:

- **Failure census** (skill §1–4) → one subagent → the per-class table with
  denominators, session concentration, and main-loop/subagent split, plus
  `first-seen`/`last-seen` for every signature class it will carry into the ranked
  list (skill §8 binds proposals, not only prior fixes).
- **Mistake census, semantic pass, and classifier evaluation** (§5–7) → one
  subagent → `pg-ccaudit report`'s ranked list verbatim plus its cost line, plus
  `pg-ccaudit evaluate`'s per-class precision/recall, `scored` count, gold-set
  labeller, and whether it exited non-zero. The classifier MUST be bounded (`--max`
  or the window) before its first model call.
- **Continuity** (§8 + prior rulings) → one subagent →
  (a) for each fix accepted in the previous retro: `first-seen`/`last-seen` and
  occurrence counts over BOTH windows, normalized to occurrences/day with the
  division shown (the windows differ in length); fixes with no error signature are
  checked against the testable claim recorded in their bead;
  (b) prior rulings: the F-9 `decided-against?` probe from `beads-lifecycle`, run
  over every candidate theme (one `--desc-contains` probe per theme — cheap, and all
  inside this one subagent), plus the rulings recorded in prior run-record bodies
  (resolve ids via the closed-record query above, read bodies with
  `bd -C /Users/phillipg/phillipg_mbp show <id> --json`).

Raw query output, per-session drill-downs, and classifier item lists stay in the
subagents; only their summary tables reach the main loop. As each phase returns:
print one progress line (`[phase] done — <elapsed>, $<spent so far>, strongest
signal so far: <one clause>`) and append a checkpoint via
`bd -C /Users/phillipg/phillipg_mbp update <id> --append-notes '<checkpoint>'`
(notes is the only append-safe field; `-d` overwrites the body) — the phase's
returned summary table verbatim plus its calls/$ subtotal, stamped with session id
and timestamp. A checkpoint recording only "done" is not a checkpoint: the resume
would re-pay for the phase. If cumulative spend exceeds 1.5× the preflight estimate,
STOP before the next phase, checkpoint, and report — MUST NOT silently continue.

Anything the operator has previously ruled out is dropped from the report and MUST
NOT be counted as correction load — but quote BOTH the raw canned-query figure (with
query name and version) and the adjusted figure, labelled as an adjustment; never
present the adjusted number as the query's output.

**Repetition** (the no-error half of the retro lens) MUST be derived from the index,
not invented: `session-concentration` rows where total ≈ distinct sessions (the
"re-learned every session" shape), retry chains, and error-then-narration prose.
True repetition with no error signal that no canned query can see → file a
missing-query bead per the skill.

## Report contract

The skill's Reporting items 1–7 apply verbatim (versioned query names, coverage line,
per-finding denominator/concentration/split, one route each, first/last-seen,
classifier provenance and gold-set labeller, unanswered questions). This command ADDS
the following structure:

```
# Improvement retro: <covered start> → <covered end>
**Method and cost** (fixed block):
  requested vs covered window + provenance | index coverage + staleness (verbatim) |
  classifier + gold-set labeller | calls + $ | truncation: <e.g. "yes at --max 100 —
  all rates below are over the truncated set"> | correction counts are a lower bound
  (file channel excluded)
## Previous retro's fixes — did they work?     fixed / improved / unchanged, occurrences/day over both windows, division shown
## Carried from last retro                      unruled items, re-presented BEFORE new findings
## Top improvements, ranked
## The load-bearing details
## Explicitly not worth acting on               with the numbers that say why
## Decisions needed
```

The heading always names the COVERED window; when it differs from the requested one,
the Method block states both.

**Top improvements** MUST paste `pg-ccaudit report`'s ranked list unmodified — it
already carries denominators, concentration, split, and route; cite its rank
numbers, MUST NOT renumber. Add per item: Size (S/M/L/XL), target artifact (the
concrete file / skill / hook / script path), and a one-line concrete fix. Routes are
the skill's eight verbatim, plus two retro-only values the report cannot emit:
`script` (a new supporting script) and `harness-fix`. Note: a fix routed to
`~/.claude/CLAUDE.md` is a nix-repo change plus apply on this machine, not an
in-session edit — size it as bead work. The failure census's per-class table is
SUPPORTING detail only: it appears inside `## The load-bearing details` under each
finding it substantiates, and MUST NOT be printed as a second ranked section.

**The load-bearing details** exists to pre-empt `?` replies: front-load mechanism,
evidence, and the concrete fix for every decision item.

**Decisions needed** MUST hold at most 7 items; the remainder go under a one-line
`## Ranked, not up for decision this round` and are re-ranked next retro. Print, as
the literal last lines of the report with nothing after them:

```
Reply on one line, e.g.:  yes: 1,3; now: 4; no: 2; ?: 6; later: 5
  yes   → I file one bead per item. No code changes.
  now   → I file the bead AND implement it in this session.
  no    → recorded as a ruling; never re-proposed. A bare `no` is a complete
          ruling ("operator rejected, no reason given" + the item's evidence);
          I ask for a reason only if I'd otherwise re-propose it, and for all
          such items in ONE question.
  ?     → I answer from the evidence already gathered — ONE follow-up message,
          no census re-entry, no new subagents — then you re-rule.
  later → no bead, no ruling; carried to next retro.
  Unmentioned numbers = later.
```

STOP and wait for the reply. If the reply does not parse cleanly, echo your
interpretation as a numbered list and ask once for confirmation — MUST NOT guess a
ruling. (The one-turn constraint above binds the census subagents; the retro itself
DOES wait across turns here.)

## After the rulings — record in few calls

- **Accepted (`yes`/`now`)**: invoke `beads-lifecycle`; then one Bash call chaining
  the `bd create`s (repo label from the target repo's own `## Beads Labels` section —
  the workspace `CLAUDE.md` table is a lookup aid, not the authority; each body
  records the item's TESTABLE CLAIM — the error signature, or a named check for
  signature-less fixes — which is what the next retro's continuity pass verifies),
  one call wiring the `bd dep` edges, reading back `bd dep list <id>` for every bead
  created with `--deps` (cross-tracker deps drop silently). Then implement `now`
  items in this session — except items sized L or XL: file the bead, say in one line
  why it is not in-session work, and ask the operator to confirm the downgrade to
  `yes`.
- **`?` unresolved at round end**: one open bead carrying the operator's constraints,
  labelled `human` (never `improvement-retro`); recorded in the run record as
  carried.
- **Close the run record**: write the final body with
  `bd -C /Users/phillipg/phillipg_mbp update <id> --body-file -`, then
  `bd -C /Users/phillipg/phillipg_mbp close <id> --reason 'retro complete; next_since=<date>'`.
  The body holds: requested window, covered window,
  `next_since = <covered window's exclusive end>` — i.e.
  `min(requested --until, the DATE of the index high-water timestamp)`, never the
  requested `--until` when the index did not reach it — total cost, the ranked
  table, and EVERY item's ruling: yes/now/no (with reason if given)/`?`-carried/
  later. This closed bead is the single canonical rulings home and the next run's
  baseline; rulings MUST NOT additionally be written to memories. If a ruling
  supersedes an instruction in some other bead's body, S-1 applies: amend that bead
  in the same exchange.
- **Abort path**: a run that dies before rulings leaves the run bead OPEN with the
  per-phase checkpoints already appended to its notes — that is what the resume
  contract in Step 0 reads. A partial run's checkpoints record its true covered
  window so the next `--since` neither skips days nor double-pays for them.

## Filed — closing block, required

```
| # | ruling | route → artifact           | id or path |
|---|--------|-----------------------------|------------|
| 3 | yes    | skill → <path>/SKILL.md     | pg2-abc12  |
Run record: <bead-id> (closed). Total: $<X> over <N> classifier calls; measured wall clock <N>m.
Next retro's default window starts <next_since>.
```
