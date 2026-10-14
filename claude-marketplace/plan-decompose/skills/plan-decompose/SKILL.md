---
name: plan-decompose
description: >-
  Use to turn an APPROVED design or plan into curated, SELF-CONTAINED implementation issues
  (work packets) that an agent can execute without reading the full design — plus to check
  whether a plan is ready for that ("is this plan ready to decompose?"), to RECONCILE
  already-decomposed packets after the design is amended, and to REPORT a single docket's
  metrics (escalation rate, validation-retry rate, actual-vs-estimated budget, stuck/released
  counts). Fires on: decompose a design/plan into beads/issues/tickets/work packets; "split
  this plan into implementation issues"; "create the epic and children from this design";
  "reconcile docket <id>"; "report on docket <id>'s metrics" / "what's the escalation rate for
  docket <id>?". Do NOT use for: designs that would yield fewer than 3 packets (below the
  floor — file the beads directly); sizing one already-scoped bead/task into a handful of
  children with no design document to cite (that is `pg-pr-break-down-work`); improving
  EXISTING issues (that is bead-grooming); finding or working ready issues (that is bd ready /
  the drain queue); writing the design itself (brainstorming/writing-plans); a workspace-wide
  metrics rollup (this mode is always scoped to one docket); a design too large to decompose in
  one pass — that is `epic-decompose`'s job, not repeated `plan-decompose` invocations against
  the same source.
---

# plan-decompose — curated, self-contained work packets from an approved design

The decomposer SPLITS and CURATES an approved design; it NEVER authors design content. The
output is a **docket** (parent issue holding the design of record) plus **work packets**
(children an implementer can execute from one read). Design of record for this skill:
`docs/superpowers/specs/2026-08-27-plan-decompose-design.md` in `phillipgreenii-nix-agent-support`
(provenance only — this skill stands alone).

**Boundary with `pg-pr-break-down-work`:** that skill sizes one already-scoped bead/task into
a handful of children — no design document, no citation requirement, no QA-agent pipeline.
This skill requires an APPROVED design/plan document to cite and has a 3-packet floor; below
that floor, or with no design document at all, use `pg-pr-break-down-work` instead.

## Concepts

- **Docket** — the parent object: the full design of record VERBATIM, a revision marker,
  the packet index, docket-wide policy metadata, and the escalation path. Never claimable
  work. An issue IS a docket iff its metadata carries `pd_rev`.
- **Work packet** — one self-contained unit of implementation work, sized to a stated model
  and ALL-IN token budget. An issue IS a packet iff its metadata carries `pd_curated_rev`.
- **All-in budget** — covers the implementer's prompts, the packet text, everything it must
  read, the implementation turns, and the change-scoped validation turns.
- **One-read target** — an implementer SHOULD be able to execute from one read of its packet.
  It is a target, not a gate: a packet needing more reads still ships, with the extra reads
  planned explicitly (`Expected additional reads: <paths>`).
- **Consumer-agnostic** — the packet text is the interface. Any consumer (drain queue,
  dispatched `packet-implementer` agent, interactive session) MUST be able to complete a
  packet from its text alone. If work succeeds only via the agents, that is a curation defect.

## Medium binding (REQUIRED)

This skill is medium-agnostic and MUST NOT run without a binding skill that maps the abstract
operations below. Resolution: use the binding named in the invocation/brief; when none is
named and exactly ONE `plan-decompose-*` binding skill is installed, auto-select it and SAY
SO; with zero or several candidates and none named, REFUSE and list the candidates. Never
fall back to ad-hoc files or a different tracker.

| Abstract operation                                       | Purpose                                                                                                                                                         |
| -------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `find-docket(design-source)`                             | Dedup/resume probe — MUST run before creating a docket                                                                                                          |
| `create-docket(design, revision, metadata)`              | Persist design of record + revision + policy; never claimable                                                                                                   |
| `create-packet(docket, content, criteria, metadata)`     | Persist one packet, HELD (invisible to ready queues) until released                                                                                             |
| `wire-ordering(blocked, blocker)`                        | Ordering edge; read-back verified; cycle-checked after bulk wiring                                                                                              |
| `read-packet(packet)` / `read-docket-design(docket)`     | Content reads (implementer path / escalation path)                                                                                                              |
| `read-metadata(obj)` / `write-metadata(obj, kv)`         | Per-key REPLACE-semantics channel; cheap independent of design size                                                                                             |
| `release-set(docket)`                                    | Make held packets claimable, recording per-packet progress                                                                                                      |
| `write-report(target, report)`                           | Append a durable report to the docket — or to a named tracking issue pre-docket                                                                                 |
| `append-metric(packet, record)` / `read-metrics(docket)` | Append-only metric records; `read-metrics` powers mode `report`, MUST scope to one docket and MUST paginate over children (never load the whole docket at once) |
| `amend-design(docket, design-or-diff, revision+1)`       | RECONCILE entry point; accepts a diff against the current design (binding resolves the merge) or, with no prior design revision, full replacement text          |
| `close-docket(docket)`                                   | Terminal close; never automatic mid-flight                                                                                                                      |

Only modes `reconcile` and `report` use `amend-design`, `append-metric` and `read-metrics`; a binding MAY map them apart from the rest (the beads binding does, in its `references/`).

## Metadata keys (per-key replace semantics; never packet content)

Keys use underscores (`pd_`), never hyphens. Values compare as strings.

| Key                                                       | On     | Meaning                                                                                             |
| --------------------------------------------------------- | ------ | --------------------------------------------------------------------------------------------------- |
| `pd_rev`                                                  | docket | Design revision; bumped by RECONCILE                                                                |
| `pd_model`, `pd_budget`, `pd_read_target`                 | docket | Sizing policy for this decomposition                                                                |
| `pd_phase`                                                | docket | Decomposition phase marker, written at EVERY transition; literals under "`pd_phase` literals" below |
| `pd_source`                                               | docket | Design-source identifier (path or issue id) for dedup                                               |
| `pd_model`, `pd_budget`                                   | packet | Deviation ONLY; absent = docket policy                                                              |
| `pd_curated_rev`, `pd_curated_date`, `pd_curated_session` | packet | Curation stamp (session = the decomposer's session id); rules under "Curation stamp" below          |
| `pd_stale`                                                | packet | Re-curation marker; values and who sets and clears them under "`pd_stale` markers" below            |

### `pd_phase` literals

`precheck` / `curating` / `prefilter` / `coldread` / `postcheck` / `wiring` /
`releasing:<n>/<m>` / `released` / `released:partial` / `reconciling:<rev>` / `failed:<phase>` —
written at EVERY transition. `released:partial` is a DISTINCT literal from `released`, not a
sub-case of it: it marks a release whose decomposition report's not-decomposed list (tracked live
since step 3/5) was non-empty AT RELEASE — this docket's OWN scope is fully covered, but it
deliberately left named design elements to a later decomposition. See mode `decompose` step 1
(routing) and step 10 (which literal gets written).

### Curation stamp

The ONE statement of the stamp rules (`pd_curated_rev`, `pd_curated_date`, `pd_curated_session`);
every other mention points here. `pd_curated_rev` is the docket revision this packet's content is
VERIFIED CURRENT against; `pd_curated_session` is the decomposer's session id.

- Within `decompose` it is WRITE-ONCE per packet: `create-packet` (mode `decompose` step 3) stamps
  it to the docket's `pd_rev` AT THAT MOMENT and nothing else in `decompose` ever rewrites it. A
  step 5/6/7 fix-loop pass revising an already-created packet's content is a content-only edit
  (the binding's "Revising an already-created packet's content"), never `create-packet` again,
  never a re-stamp, and never any `write-metadata` touch to `pd_curated_rev`: a fix-loop pass is
  not a curation revision, however many rounds run.
- ONLY mode `reconcile` changes it again: for AFFECTED packets at re-curation (its step 3, which
  HOLDS them until its step 5 releases them), and for UNAFFECTED packets after the affectedness
  check (its step 5, which runs only once the reconcile completes — an aborted or interrupted
  reconcile restamps no unaffected packet). So after a COMPLETED reconcile every non-closed
  packet is at the docket's `pd_rev` or carries `pd_stale`.
- An unaffected restamp changes `pd_curated_rev` only; `pd_curated_date`/`pd_curated_session`
  keep recording the last content curation.

### `pd_stale` markers

Set by a stamp-mismatch release (`<found-rev>`, above the packet's own stamp; never overwrites an
existing value), by RECONCILE on a claimed affected packet (`reconcile-pending`), and by
RECONCILE as the HOLD marker on every packet it re-curates (`<new-rev>`, equal to the packet's
own `pd_curated_rev`; an abort leaves it in place). Cleared only by a completing reconcile's
step 5 (`references/reconcile.md`).

**Sizing resolution:** packet metadata → docket metadata → the fallback defaults. The
fallback defaults — the ONLY place models or budgets are hardcoded — are: implementer
**model `sonnet`, budget `250k` tokens, `pd_read_target` `25`** (percent of budget for fixed
reads); cold-reader sub-agents **`haiku`**; the semantic post-check sub-agent **`sonnet`**.
Retargeting a decomposition is one docket metadata edit; a dispatcher MAY override the
sub-agent tiers in its brief. A malformed/missing value is treated as absent (falls through)
and MUST be flagged in the next report. `pd_rev` starts at `1` when the docket is created.

**Tuning review** (`pg2-oupz5`, 2026-09-03): `pd_read_target=25` and mode `check`'s decomposition
floor were reviewed against real dockets and CONFIRMED unchanged; rationale is in the design of
record's Open items section.

**Metric record** (append-only, single line, fixed key order — emitted by implementers at
closeout): `pd_metrics outcome=<done|blocked|released> escalation-reads=<n> validation-retries=<n> tokens=<k>`

## Work-packet content anatomy

Nine parts, in this order. EVERY substantive clause in parts 2, 3, 5, and 7 MUST end with the
citation marker `[design: <section number or heading>]` — content with no citable design
source is invention and MUST NOT be written (state the gap instead). Verbatim-copied blocks
cite their source section once for the block.

1. **Shared preamble** — global constraints / docket-wide normative text binding this packet,
   VERBATIM, byte-identical and identically ordered across the siblings that share it.
2. **Objective** — the end-to-end deliverable, one paragraph, rewritten curation.
3. **Contract** — _Consumes:_ exact signatures/interfaces used (from predecessor packets or
   existing code). _Produces:_ names, types, behaviors later packets rely on. Neighbors
   communicate ONLY through contracts; sibling packet text is never authority.
4. **Files** — create/modify/test paths; repo root stated once.
5. **Binding decisions** — decided-hows from the design (mandated algorithms, idioms, error
   shapes, libraries), VERBATIM. Decision-encoding snippets (schema, state machine) allowed;
   implementation code is not. State the freedom boundary explicitly: anything the design did
   not decide is the implementer's choice.
6. **Validation (change-scoped)** — exact commands, timeouts, expected results for THIS
   change. NEVER the full pre-landing/pre-push gate. If this packet wires a new or changed
   scheduled/dispatched consumer (a cron/launchd trigger, an event-router query/role pair, a
   message-queue handler, etc.), Validation MUST include one live, end-to-end trigger of the
   wiring itself, confirming a real non-trivial outcome — a build/eval check alone (`nix build`,
   `nix flake check`, compile) proves the config evaluates, not that the wired invocation does
   anything when actually dispatched. Do not defer this live check to a separate, later
   review/smoke-test packet: this class of wiring bug typically fails silently (no crash, no
   alert), so deferring it lets a broken wiring run unnoticed in production (incident:
   `pg2-93e5s`/`pg2-cjwfu`, 2026-09-23 — a flake-check-clean role wiring silently no-op'd every
   scheduled tick).
7. **Acceptance criteria** — independently verifiable checklist (bead-grooming altitude for
   the issue type); stored in the medium's dedicated acceptance field.
8. **Out of scope** — names the sibling packet holding each neighboring concern.
9. **Lifecycle bounds + escalation pointer** — one line each: "this packet = implement +
   validate; isolation/landing/cleanup/claim hygiene are consumer-standard" (state deviations
   explicitly); and the docket id with: _read the docket design ONLY when stuck, and record
   that you did._ Add `Expected additional reads: <paths>` when a needed read-set is too big
   to inline.

Hoisting: content common to all packets lives once and is never copied into packets. Its
home depends on what it is: packet-facing discipline (stamp check, packet-first, escalation,
metric closeout) → the `packet-implementer` agent prompt; decomposition policy (sizing,
revision) → docket metadata; CONSUMER-STANDARD workflow (isolation, landing, cleanup,
claim/close hygiene) → deliberately NOWHERE in this plugin — "consumer-standard" MEANS
defined by the consuming environment's own standing rules (queue commands, workspace
always-on agent rules), and packets/agents only point at it. Docket-specific normative TEXT
common to all packets is the one sanctioned repetition (part 1) — self-containment outranks
deduplication.

## Mode `check` — is this plan ready to decompose?

1. Produce a **boundary sketch**: packet titles + one-line scopes ONLY (no curation).
2. **Floor verdict**: sketch < 3 packets ⇒ "below floor — file the bead(s) directly"
   (decomposition overhead exceeds value); 3–4 ⇒ "marginal — operator's call"; else "clear".
3. **Decomposability pre-check** against the sketch: for each sketched packet — seam
   contracts decided? file/package layout stated? validation approach stated? behavior
   precise enough for verifiable criteria? Mechanical scans: TBD/TODO/placeholder markers;
   open-items sections intersecting packet scope. Judgment probe: could two reasonable
   implementers, given only this plan, build incompatible halves of a seam?
4. Output on PASS: verdict + the sketch. On gaps: a **gap report** — per gap: what is
   missing, which anatomy part it blocks, where in the design it should live. The gap report
   goes to the DISPATCHER of record — the session or agent that invoked this skill (in
   interactive use, the operator's session) — and when a tracking issue is named (the brief
   SHOULD name one) it is ALSO written there via `write-report` so it survives the session.

## Mode `decompose`

- **0a.** Invoke `plan-decompose:plan-sanity-check` inline via the Skill tool, at level `plan`,
  before mode `check` runs — the same shared pre-gate `epic-decompose` and `phase-decompose`
  invoke at their own levels (`raw-epic` and `phase` respectively). On `good_enough: no`, halt
  and surface `reasons`, subject to the same explicit-operator-override as step 0's below-floor
  halt.

0. Resolve the binding; resolve sizing policy (the brief's values, else the Metadata-keys
   fallback defaults); run mode `check`. Halt on gaps or below-floor unless the dispatching
   conversation/brief carries an explicit operator override. This is the primary sanctioned
   halt: decomposing around a gap creates packets whose neighbors do not exist.

1. `find-docket(design-source)`: an existing docket for this source ⇒ MUST NOT create a
   second. Routing rule: `pd_phase=released` (full coverage, literal match — see below) and
   the design text matches the stored design ⇒ nothing to do (report the existing docket);
   `pd_phase=released:partial` and the text matches ⇒ this is NOT "nothing to do" — read the
   docket's not-decomposed list (its most recent `write-report` decomposition report) and
   report it to the caller as still-open scope; direct the caller to decompose it under a
   NEW, scope-qualified `pd_source` — `<design-source>#remainder<n>` (query existing dockets'
   `pd_source` values for the next unused `<n>` on this prefix) — never by re-invoking against
   the unqualified `design-source`, which loops back to this same branch, and never by writing
   into THIS docket, which is already fully released for its own scope. Mirrors
   `epic-decompose`'s `<program-epic>#phase<n>` minting, scoped to one docket's leftover slice
   instead of a whole program epic's phase split. Either `released` or `released:partial`,
   with the text DIFFERING from the stored design ⇒ mode `reconcile` (reconcile step 2
   re-evaluates and may change which of the two literals the docket ends up at, restored in
   its step 6 — and an aborted reconcile, per its step 4, restores neither); any other
   `pd_phase` ⇒ RESUME from that phase — and if the design ALSO changed since, re-run the
   pre-check on the new text first and resume curation against it.
   `pd_phase` comparison MUST be an exact-string match, never a substring/prefix test —
   `released:partial` contains `released` as a substring and a prefix-matching routing check
   would silently mis-route a partial release into the "nothing to do" branch.

   **RESUME dedup** (`pg2-b439c`; widened `tc-96czz`): a RESUME back into step 3 MUST NOT re-run
   curation blind. The per-`create-packet` check that prevents it is stated once, in step 3.

2. Create the docket (design VERBATIM + `pd_rev` + policy + `pd_source`); set `pd_phase` at
   every transition from here on.
3. **Curate** each packet per the anatomy, packets created HELD.

   **Draft workflow**: draft each packet's content into scratch files with the Write tool and
   make fix-loop revisions to them with the Edit tool — never a full-content Bash heredoc rewrite
   of the whole packet. Bash stays for this plugin's helper scripts ("Helper scripts") and your
   own mechanical pre-filter checks.

   **RESUME dedup** (`pg2-b439c`; widened `tc-96czz` — a check performed once at step-3 ENTRY
   cannot catch a concurrent PEER that finishes decomposing the same docket between that entry
   and a LATER `create-packet` call in the same pass; that gap let two concurrent
   `phase-decompose` runs both fully curate the same docket from the same trigger): a RESUME back
   into step 3 MUST NOT re-run curation blind, and no entry into step 3 — first pass, resume, or
   otherwise — MAY perform this check ONCE and then reuse that result across more than one
   `create-packet` call. Step 0 unconditionally re-runs mode `check` and produces a fresh
   boundary sketch every time `decompose` is invoked, including on a RESUME — so an interrupted
   attempt's sketch and the RESUME's freshly re-generated sketch cover the same design but are
   not guaranteed byte-identical in title wording. **Immediately before EACH individual
   `create-packet` call** — with a FRESH read taken at that moment, never a children list
   fetched earlier in this run (or earlier in this same step-3 pass) and reused — list the
   docket's current children (the binding's "Children list" read) and, for each existing child,
   check its content's `[design: <section>]` citation footprint against the planned packet's
   scope — MATCH ON CITED DESIGN SECTIONS, never on title text: sections are the design-derived
   identity a slice keeps across re-sketches, titles are not. A planned packet whose scope
   already has a matching existing child (any status — open, deferred, or closed, and including
   one THIS SAME PASS already created for an earlier planned packet in the batch) MUST NOT be
   created again; treat it as already curated (mode `reconcile`'s re-curation path handles it if
   the design changed since, never a fresh `create-packet`). Only a slice whose just-taken check
   finds no existing match gets created, and that `create-packet` call MUST follow immediately —
   no other planned packet's check or creation interleaved between this check and this call.
   This check applies immediately before EVERY `create-packet` call, not once per step-3 entry
   and not once per batch of planned packets — it is what stops an interrupted
   `curating`/`failed:curating` run, AND a concurrent peer that completes the same docket
   mid-batch, from producing duplicate packets for the same design slice.

   `create-packet` runs EXACTLY ONCE per packet, immediately after that packet's own just-taken
   dedup check finds no match, and stamps `pd_curated_rev` to the docket's CURRENT `pd_rev` (read
   once, not re-read per packet); the stamp rule is "Curation stamp" under Metadata keys. A
   RE-ENTRY into this step from step 5's, step 6's, or step 7's loop-back revises an
   ALREADY-CREATED packet's CONTENT only (the binding's "Revising an already-created packet's
   content"). Record the PLANNED ORDERING (blocked-by pairs implied by Consumes/Produces) in the
   decomposition-report draft as you go — the draft is your working state (in-context or a
   scratch file); it becomes durable only via `write-report`, and the abort path writes its
   current content. Boundary rule: split only where a reviewer could reject one half while
   approving the other; otherwise fold. After the last packet: resolve blocks common to ALL
   packets — delete restated consumer discipline; keep docket-specific normative text as the
   shared preamble.

4. **Size** each packet: fixed inputs (packet text + expected read-set, bytes ÷ 4) MUST be
   ≤ `pd_read_target`% of budget. Over ⇒ split. Unsplittable (no boundary passes the
   reviewer-reject test) ⇒ write a packet-metadata deviation and PROCEED — sizing never
   halts. Estimates go in the report, not packet content.
5. **Mechanical pre-filter** — checks YOU run yourself with shell tools over the drafted
   packet texts (no agent dispatch, no shipped script; near-zero cost; gates the agent
   dispatches): uncited clauses (grep for the `[design:` marker per substantive clause);
   file-overlap collisions (extract each packet's Files paths, intersect, require a planned
   edge between any two packets sharing a path); out-of-scope pointer validity (each "that is
   packet X" claim checked against X's Objective); shared-preamble byte-identity (diff the
   part-1 blocks); metadata completeness (stamps, policy, `pd_rev`); citation-target
   existence (every `[design: <section>]` marker's target actually exists in the design
   text — this catches a citation to a NONEXISTENT section, never a citation to a real
   section that doesn't support the clause, which stays the semantic post-check's call);
   whole-set coverage scan, ADVISORY ONLY (every design section/heading is named by at least
   one citation somewhere across the full packet set; a section cited by zero packets is
   reported, never a pre-filter failure — cross-check the in-flight decomposition-report
   draft's not-decomposed list, updated at the moment step 3 excludes a section, not deferred
   to report time); ported-file-list check (diff each packet's named source-to-port files
   against `git ls-files <source-dir>`; a named path absent from the source is blocking, a
   real source file never named by any packet is advisory); forbidden cross-module import
   (grep cited import paths for any `/internal/` segment; flag any citing packet whose
   destination file path is not itself nested under the directory containing that segment);
   AC-to-Validation cross-reference (match on the BASE command name, not the full
   backtick-quoted string, reading both the `--acceptance` field and the Validation part;
   flag blocking only when the base command name never appears anywhere in that packet's
   Validation part at all). The uncited-clause check above also covers part 7 now (§6.1's
   citation parts widened to include Acceptance criteria) — that widening is this list's
   conceptual sixth item, not a new clause of its own. A block found byte-identical across ALL
   packets that step 3 did not fold raises a hoisting flag (advisory, reported, never
   blocking). Failures loop to step 3 (content-only; see "Curation stamp").
6. **Cold-read check** (one cheap-model agent per packet, read-only): input is the packet's
   stored text only, delivered by file path; output `executable: yes|no` + `missing:` list,
   plus any assumption the reader had to make about a sibling packet's Consumes/Produces shape
   that is not pinned by the design or a predecessor's Produces. Dispatch per "Sub-dispatch
   templates" (Cold-read). Any packet EDITED after its cold-read MUST be cold-read again AND
   re-sized — an editor holding the full design cannot certify self-containment. This edit is
   content-only, per step 3 (see "Curation stamp").
7. **Semantic post-check** (one mid-model fresh-eyes agent, read-only; the one role that
   reads the full design AND all packets): (a) coverage both directions — every design
   element lands in a packet or is recorded via `write-report` as deliberately not
   decomposed (this is the list step 10 checks to decide `released` vs `released:partial`,
   and step 1 later surfaces to a `find-docket` caller — get it right here, not just at report
   time); every citation resolves to design text that supports its clause; (b) seam
   consistency — every Consumes supplied by a planned predecessor's Produces or existing
   code, signatures matching.

   Severity rubric — `blocking` = a silent behavior gap, a missing producer for a contract
   the packet set already commits to, or a seam that would misroute or lose data; `minor` =
   everything else — a clarity gap, a redundant or currently-vacuous check, or a citation
   that's off-target but doesn't change the clause's correctness.

   Dispatch per "Sub-dispatch templates" (Semantic post-checker). Round 1 reads the full design
   and the full packet set; each fix-loop re-check after round 1 SCOPES to the packets the prior
   round's findings touched plus their direct planned-ordering neighbors — mirroring reconcile
   step 4 (`references/reconcile.md`) — never the full design and full packet set again. State
   the round's total prompt byte-length in the decomposition report (step 10) so a non-scoped
   round is visible immediately. Findings loop to step 3 (content-only; see "Curation stamp" —
   no re-stamp per round). All findings get fixed regardless of severity; whether a further
   round is dispatched, and what happens at the cap, is governed by step 8's rule.

8. **Loop bounds** (no loop is unbounded): a `blocking` finding recurring on the same packet
   for the same check against the same evidence target a SECOND time ⇒ treat as a gap — halt
   that packet (continue the rest only if no seam depends on it, else abort). A recurring
   `minor` finding halts nothing.

   **Canonical gate-and-cap rule** (this is the ONE literal copy; every other mention —
   step 7's cross-reference, design §8.10, §9.1's table cell, the agent files'
   charters — says "per step 8," never restates it): the semantic post-check's fix loop
   dispatches a further round ONLY when the PRIOR round produced at least one `blocking`
   finding; a round whose findings are all `minor` is fixed and the set proceeds straight to
   release with no further dispatch. The blocking-gated loop runs at most 2 rounds. At the
   cap: if every outstanding `blocking` finding has a stated, applied fix and only
   re-verification is missing, the set RELEASES, with a residual-risk entry in the
   decomposition report naming each unverified fix. Only an UNRESOLVED or oscillating
   `blocking` finding at the cap takes the abort path ("did not converge", naming it).
   Cold-read's own bound (same finding twice on a packet ⇒ gap) is UNCHANGED and unaffected
   by any of this.

9. **Wire** the planned ordering (blocked-by direction verified by read-back on EVERY edge; a
   failed read-back retried once then treated per step 8; cycle check after bulk wiring,
   output filtered to this docket's packets). At least one packet must be immediately
   workable when the count is ≥ 1 — unless the only blockers are EXTERNAL (cross-docket)
   edges, which is legitimate: report it, do not treat it as a failure.
10. **Release** the set (`pd_phase=releasing:<n>/<m>` as the sweep proceeds, then `released`
    if the not-decomposed list is empty at this moment, else `released:partial`) and
    `write-report` the decomposition report: packet index, per-packet fixed-read estimates
    and QA dispatch counts (cold-reads per packet, post-check rounds), each semantic
    post-check round's prompt byte-length, sizing deviations, check outcomes, hoisting flags,
    not-decomposed records, and an explicit "no uncited content" assertion. Get the
    not-decomposed list right here — it is both what step 1's routing rule reports back to a
    later `find-docket` caller and what decides which of the two `pd_phase` literals gets
    written.

**Abort path (any early exit):** leave all packets DEFERRED — NEVER release an unverified
set — set `pd_phase=failed:<phase>`, and `write-report` what was completed. The decomposer
holds no claims (packets are deferred, not assigned), so claim hygiene is satisfied by
construction. A deferred packet under a docket whose `pd_phase` is not `released` is
mid-decomposition by definition — report it, never steal or force-release it.

## Helper scripts

The Skill tool's result for `plan-decompose:plan-decompose` names this skill's own base directory
(ending `.../plan-decompose/skills/plan-decompose`); a caller that runs `decompose` inline via
the Skill tool (`phase-decomposer`) gets the same result. This plugin's helper scripts live at
`<that base directory>/../../scripts/` — the plugin root's `scripts/` sibling of `agents/` and
`skills/`. Resolve the path once from the reported base directory and reuse it for the rest of
the run; never `find` or `glob` the filesystem for a script by name — the base directory the
Skill tool just reported is a full, working answer to "where is my own plugin," and re-deriving
it by searching disk is both slower and redundant.

- `scripts/chunk-for-bd-field.sh <input-file> <output-prefix>` — splits a file into
  line-safe, byte-capped chunks (default 65000 bytes, safely under bd's 65,535-byte field cap)
  and prints the chunk paths, one per line. Run it ONCE per oversized document (the design, a
  packet body, a report) instead of computing byte lengths and slicing text yourself by hand —
  every chunking step in this skill and its bindings means "run this script," not ad hoc
  `wc -c`/`sed`.
- `scripts/create-packet.sh` — this IS the `create-packet` operation for the `beads` binding
  (see `plan-decompose-beads/SKILL.md`'s `create-packet` section): never hand-type a
  `bd create`/`bd defer` pair for a packet yourself. It bakes `--no-inherit-labels` in as the
  default so the flag can no longer be dropped by accident (`--allow-inherit-labels` opts back
  in explicitly).
  ALWAYS pass `--actor <your-session-id>[-<role>]` (your own explicit agent identity, the same one
  you claim with): the script forwards it to both its `bd create` and `bd defer`, so packets are
  attributed to you and not to the operator's git `user.name`.
- `scripts/audit-docket-label-leak.sh` — read-only; run it once after each batch of
  `create-packet` calls, before `release-set`. It calls
  `bd list --label docket --status all -n 0 --json` and flags every returned bead whose
  `issue_type` is not `epic` — a packet that inherited the docket epic's `docket` label because
  its `create-packet` call omitted `--no-inherit-labels` (this has happened more than once;
  `bd label remove <id> docket` fixes a flagged bead). Exit 3 means it found leaks; exit 0 means
  clean.

## Sub-dispatch templates

Use these prompt shapes verbatim so runs are comparable. Sub-dispatches go through the Agent tool
ONLY — never a headless CLI subprocess such as `claude -p` — and carry the brief's absolute repo
root(s). Both sub-agents are READ-ONLY and MUST report fully in one turn (no waiting, no Monitor,
no further sub-agents). The cold-reader agent file states that itself; for the post-checker,
state the constraint in the prompt itself, since a fresh sub-agent has no other way to know it.

**Cold-read** (mode `decompose` step 6; one Agent call per packet): `subagent_type:
"plan-decompose:cold-reader"` (`model` only when the brief overrides the haiku tier); the prompt
is ONLY two paths — the packet's body file and acceptance file, the same files passed to
`create-packet.sh` (`--body-file`, `--acceptance-file`) or to the fix-loop `bd update` (criteria
passed inline are first written to an acceptance file), so the reader judges the stored text.
Never paste packet text into the prompt; the reader's own agent file (`agents/cold-reader.md`)
carries its instructions. Dispatch one round's cold-reads as multiple Agent tool_use blocks in a
SINGLE turn — never one dispatch-and-wait per turn — and never re-read a packet not edited since
its last cold-read.

**Semantic post-checker** (mode `decompose` step 7; one Agent call per fix round; `subagent_type:
"plan-decompose:semantic-post-checker"`, `model: sonnet` — that agent is read-only by tool
grant, not just by instruction). Two dispatch variants — NEVER re-send the full design or full
packet set on round 2+:

> Round 1: `[full design] [all packets] [planned ordering] [repo roots]`.
>
> Round 2+: `[only the design sections the prior round's findings cited] [only the packets
those findings touched, plus their direct planned-ordering neighbors] [planned ordering]
[repo roots]`.

(the semantic-post-checker agent's own prompt already states its task and output format; your
dispatch supplies only these four inputs.)

## Other modes

Mode `check` and mode `decompose` are above. The other two modes live in `references/` so a
`decompose` run never loads them; READ the file before running that mode (it names the binding's
`references/reconcile-and-report.md` mappings that mode also needs):

- mode `reconcile` — including "Stamp catch-up (no amendment)", "Resuming `reconciling:<R>`" and
  "Stamp-mismatch releases" → `references/reconcile.md`;
- mode `report` → `references/report.md`.

## Consumers

Packets work with zero consumer changes: drain-style pointer briefs ("read your issue
yourself") land on a self-contained packet. The optimized path is the `packet-implementer`
agent (this plugin): stamp check at claim, packet-first work, escalation ladder, metric
closeout. Decomposition itself is executed by the `plan-decomposer` agent so a context-heavy
session can hand it off. Recorded escalation reads and validation retries (the metric
records) are the curation-quality signal, aggregated on demand — scoped to one docket at a
time, paginated over its children — via mode `report`.

## Usage

- "is `<design doc / bead>` ready to decompose?" → mode `check`.
- "decompose `<design>` into beads" (optionally sizing policy, tracking issue) → dispatch
  `plan-decomposer` with: design source, binding, absolute repo root(s), docket metadata,
  tracking issue. Progress: docket `pd_phase` + report comments.
- "the design for docket `<id>` changed — reconcile" → mode `reconcile` with a diff against
  the current design, or full text when the docket has no design revision yet.
- "catch up docket `<id>`'s stamps" / "reconcile docket `<id>` with no amendment" (a
  packet-implementer reported "reconcile owed" but the design did not change) → mode
  `reconcile`'s stamp catch-up.
- "report on docket `<id>`'s metrics" / "what's the escalation rate for docket `<id>`?" →
  mode `report`, run directly against that one docket (no agent dispatch required — it is
  read-only and paginated, safe for the invoking session itself).
