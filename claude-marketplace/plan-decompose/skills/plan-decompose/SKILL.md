---
name: plan-decompose
description: >-
  Use to turn an APPROVED design or plan into curated, SELF-CONTAINED implementation issues
  (work packets) that an agent can execute without reading the full design — plus to check
  whether a plan is ready for that ("is this plan ready to decompose?"), and to RECONCILE
  already-decomposed packets after the design is amended. Fires on: decompose a design/plan
  into beads/issues/tickets/work packets; "split this plan into implementation issues";
  "create the epic and children from this design"; "reconcile docket <id>". Do NOT use for:
  designs that would yield fewer than 3 packets (below the floor — file the beads directly);
  improving EXISTING issues (that is bead-grooming); finding or working ready issues (that is
  bd ready / the drain queue); writing the design itself (brainstorming/writing-plans).
---

# plan-decompose — curated, self-contained work packets from an approved design

The decomposer SPLITS and CURATES an approved design; it NEVER authors design content. The
output is a **docket** (parent issue holding the design of record) plus **work packets**
(children an implementer can execute from one read). Design of record for this skill:
`docs/superpowers/specs/2026-08-27-plan-decompose-design.md` in `phillipgreenii-nix-agent-support`
(provenance only — this skill stands alone).

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

| Abstract operation                                       | Purpose                                                                         |
| -------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `find-docket(design-source)`                             | Dedup/resume probe — MUST run before creating a docket                          |
| `create-docket(design, revision, metadata)`              | Persist design of record + revision + policy; never claimable                   |
| `create-packet(docket, content, criteria, metadata)`     | Persist one packet, HELD (invisible to ready queues) until released             |
| `wire-ordering(blocked, blocker)`                        | Ordering edge; read-back verified; cycle-checked after bulk wiring              |
| `read-packet(packet)` / `read-docket-design(docket)`     | Content reads (implementer path / escalation path)                              |
| `read-metadata(obj)` / `write-metadata(obj, kv)`         | Per-key REPLACE-semantics channel; cheap independent of design size             |
| `release-set(docket)`                                    | Make held packets claimable, recording per-packet progress                      |
| `write-report(target, report)`                           | Append a durable report to the docket — or to a named tracking issue pre-docket |
| `append-metric(packet, record)` / `read-metrics(docket)` | Append-only metric records (gathering only; aggregation is future work)         |
| `amend-design(docket, design, revision+1)`               | RECONCILE entry point                                                           |
| `close-docket(docket)`                                   | Terminal close; never automatic mid-flight                                      |

## Metadata keys (per-key replace semantics; never packet content)

Keys use underscores (`pd_`), never hyphens. Values compare as strings.

| Key                                                       | On     | Meaning                                                                                                                                                                               |
| --------------------------------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pd_rev`                                                  | docket | Design revision; bumped by RECONCILE                                                                                                                                                  |
| `pd_model`, `pd_budget`, `pd_read_target`                 | docket | Sizing policy for this decomposition                                                                                                                                                  |
| `pd_phase`                                                | docket | `precheck` / `curating` / `prefilter` / `coldread` / `postcheck` / `wiring` / `releasing:<n>/<m>` / `released` / `reconciling:<rev>` / `failed:<phase>` — written at EVERY transition |
| `pd_source`                                               | docket | Design-source identifier (path or issue id) for dedup                                                                                                                                 |
| `pd_model`, `pd_budget`                                   | packet | Deviation ONLY; absent = docket policy                                                                                                                                                |
| `pd_curated_rev`, `pd_curated_date`, `pd_curated_session` | packet | Curation stamp (session = the decomposer's session id)                                                                                                                                |
| `pd_stale`                                                | packet | Set by a stamp-mismatch release or by RECONCILE on a claimed packet; cleared by re-curation                                                                                           |

**Sizing resolution:** packet metadata → docket metadata → the fallback defaults. The
fallback defaults — the ONLY place models or budgets are hardcoded — are: implementer
**model `sonnet`, budget `250k` tokens, `pd_read_target` `25`** (percent of budget for fixed
reads); cold-reader sub-agents **`haiku`**; the semantic post-check sub-agent **`sonnet`**.
Retargeting a decomposition is one docket metadata edit; a dispatcher MAY override the
sub-agent tiers in its brief. A malformed/missing value is treated as absent (falls through)
and MUST be flagged in the next report. `pd_rev` starts at `1` when the docket is created.

**Metric record** (append-only, single line, fixed key order — emitted by implementers at
closeout): `pd_metrics outcome=<done|blocked|released> escalation-reads=<n> validation-retries=<n> tokens=<k>`

## Work-packet content anatomy

Nine parts, in this order. EVERY substantive clause in parts 2, 3, and 5 MUST end with the
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
   change. NEVER the full pre-landing/pre-push gate.
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

0. Resolve the binding; resolve sizing policy (the brief's values, else the Metadata-keys
   fallback defaults); run mode `check`. Halt on gaps or below-floor unless the dispatching
   conversation/brief carries an explicit operator override. This is the primary sanctioned
   halt: decomposing around a gap creates packets whose neighbors do not exist.
1. `find-docket(design-source)`: an existing docket for this source ⇒ MUST NOT create a
   second. Routing rule: `pd_phase=released` and the design text matches the stored design ⇒
   nothing to do (report the existing docket); `released` and the text differs ⇒ mode
   `reconcile`; any other `pd_phase` ⇒ RESUME from that phase — and if the design ALSO
   changed since, re-run the pre-check on the new text first and resume curation against it.
2. Create the docket (design VERBATIM + `pd_rev` + policy + `pd_source`); set `pd_phase` at
   every transition from here on.
3. **Curate** each packet per the anatomy, packets created HELD. Record the PLANNED ORDERING
   (blocked-by pairs implied by Consumes/Produces) in the decomposition-report draft as you
   go — the draft is your working state (in-context or a scratch file); it becomes durable
   only via `write-report`, and the abort path writes its current content. Boundary rule:
   split only where a reviewer could reject one half while approving the other; otherwise
   fold. After the last packet: resolve blocks common to ALL packets — delete restated
   consumer discipline; keep docket-specific normative text as the shared preamble.
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
   part-1 blocks); metadata completeness (stamps, policy, `pd_rev`). A block found
   byte-identical across ALL packets that step 3 did not fold raises a hoisting flag
   (advisory, reported, never blocking). Failures loop to step 3.
6. **Cold-read check** (one cheap-model agent per packet, read-only): input is the packet
   content ONLY; output `executable: yes|no` + `missing:` list. Any packet EDITED after its
   cold-read MUST be cold-read again AND re-sized — an editor holding the full design cannot
   certify self-containment.
7. **Semantic post-check** (one mid-model fresh-eyes agent, read-only; the one role that
   reads the full design AND all packets): (a) coverage both directions — every design
   element lands in a packet or is recorded via `write-report` as deliberately not
   decomposed; every citation resolves to design text that supports its clause; (b) seam
   consistency — every Consumes supplied by a planned predecessor's Produces or existing
   code, signatures matching. Findings loop to step 3.
8. **Loop bounds** (no loop is unbounded): a finding recurring on the same packet for the
   same check against the same evidence target a SECOND time ⇒ treat as a gap — halt that
   packet (continue the rest only if no seam depends on it, else abort). The full fix loop
   runs at most 3 rounds; the 4th IS the finding: abort with a "did not converge" report
   naming the oscillating findings.
9. **Wire** the planned ordering (blocked-by direction verified by read-back on EVERY edge; a
   failed read-back retried once then treated per step 8; cycle check after bulk wiring,
   output filtered to this docket's packets). At least one packet must be immediately
   workable when the count is ≥ 1 — unless the only blockers are EXTERNAL (cross-docket)
   edges, which is legitimate: report it, do not treat it as a failure.
10. **Release** the set (`pd_phase=releasing:<n>/<m>` as the sweep proceeds, then `released`)
    and `write-report` the decomposition report: packet index, per-packet fixed-read
    estimates and QA dispatch counts (cold-reads per packet, post-check rounds), sizing
    deviations, check outcomes, hoisting flags, not-decomposed records, and an explicit "no
    uncited content" assertion.

**Abort path (any early exit):** leave all packets DEFERRED — NEVER release an unverified
set — set `pd_phase=failed:<phase>`, and `write-report` what was completed. The decomposer
holds no claims (packets are deferred, not assigned), so claim hygiene is satisfied by
construction. A deferred packet under a docket whose `pd_phase` is not `released` is
mid-decomposition by definition — report it, never steal or force-release it.

## Mode `reconcile`

Input: a docket + the amended design text. Order is load-bearing:

1. Run the pre-check on the AMENDED design FIRST. Gaps ⇒ gap report, no amendment applied.
2. `amend-design` (bump `pd_rev`; superseded text struck/rewritten, ruling recorded — never
   two live instructions). Set `pd_phase=reconciling:<new-rev>`; restore `released` when done.
3. Re-curate affected OPEN, UNCLAIMED packets; restamp (`pd_curated_rev`); clear `pd_stale`.
   "Affected" is determined by the citation markers: a packet is affected iff any of its
   `[design: <section>]` citations points into an amended section. A packet actively CLAIMED
   MUST NOT be rewritten — set `pd_stale=reconcile-pending` on it; the implementer's
   checkpoints catch it.
4. Re-run the pre-filter + cold-read on re-curated packets and a semantic pass SCOPED to the
   amended sections, the re-curated packets, and their direct planned-ordering neighbors.
   The full-set semantic check is for initial release and deliberate audits only.

**Stamp-mismatch releases** (a consumer found rotted curation first): the release MUST
re-DEFER the packet and set `pd_stale=<found-rev>` — never return it to the open pool, or
every queue consumer claims/checks/releases it in an endless cross-session spin. RECONCILE
clears `pd_stale` and undefers; the docket report channel tells the operator a reconcile is
owed.

## Consumers

Packets work with zero consumer changes: drain-style pointer briefs ("read your issue
yourself") land on a self-contained packet. The optimized path is the `packet-implementer`
agent (this plugin): stamp check at claim, packet-first work, escalation ladder, metric
closeout. Decomposition itself is executed by the `plan-decomposer` agent so a context-heavy
session can hand it off. Recorded escalation reads and validation retries (the metric
records) are the curation-quality signal; aggregation/reporting is future work.

## Usage

- "is `<design doc / bead>` ready to decompose?" → mode `check`.
- "decompose `<design>` into beads" (optionally sizing policy, tracking issue) → dispatch
  `plan-decomposer` with: design source, binding, absolute repo root(s), docket metadata,
  tracking issue. Progress: docket `pd_phase` + report comments.
- "the design for docket `<id>` changed — reconcile" → mode `reconcile` with the amended text.
