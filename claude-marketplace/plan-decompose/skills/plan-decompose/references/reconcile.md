<!-- Loaded by plan-decompose SKILL.md "Other modes" before running mode `reconcile`. Mode `decompose`'s steps are in SKILL.md; every "mode `decompose` step N" below means that file. The beads mappings for `amend-design` and "Reconcile restamps" are in plan-decompose-beads `references/reconcile-and-report.md`. -->

## Mode `reconcile`

Input: a docket + either a diff against the docket's current design or, when the docket
carries no prior design revision yet, full replacement text — or NO amendment at all, for a
stamp catch-up (below). The binding's `amend-design` RESOLVES an amendment into the full
amended design text (patch-applying a diff, or using full text as-is) before anything is
written to the docket. Below, `R` is the docket's `pd_rev` once step 2 has run (the new
revision). Revisions are integers and compare NUMERICALLY, for equality and order alike,
after normalizing both sides (the medium may store a number or a string). Order is
load-bearing:

1. Run the pre-check on the RESOLVED amended design FIRST — the binding's `amend-design`
   Resolve step, not yet committed to the docket. Gaps ⇒ gap report, no amendment applied.
2. `amend-design`'s Commit step (bump `pd_rev`; superseded text struck/rewritten, ruling
   recorded — never two live instructions). Set `pd_phase=reconciling:<new-rev>`; restore
   `released` or `released:partial` in step 6 — whichever the not-decomposed list is
   currently at (step 3's re-curation may add to it or resolve entries from it; never assume
   the pre-reconcile literal still holds without re-checking the list).
3. **Classify, then re-curate and HOLD the affected.** Classify EVERY not-closed packet under
   the docket (open, deferred, or claimed) as AFFECTED or UNAFFECTED. A packet is AFFECTED
   iff any of:
   - any of its `[design: <section>]` citations points into a section amended by ANY revision
     after its own `pd_curated_rev`, up to and including `R`. For a packet at `R`−1 that is
     just this run's own amendment; a packet stamped lower (a docket reconciled before step 5
     existed, or a leftover of an earlier claimed/aborted case) is checked against every
     intervening revision too. Revision `k`'s amended sections come from the amendment itself
     for this run's own revision, and otherwise from the report of the run that bumped
     `pd_rev` to `k` (step 6's `pd_rev <k-1> -> <k>` report, completed or ABORTED) — never
     from a catch-up's report — or from a diff of the two revisions' design texts when both
     are recoverable (e.g. from the design source's version history). A revision whose
     amended sections cannot be established either way makes every packet stamped below it
     AFFECTED;
   - any of its citations names a section that no longer exists at `R` (removed, renamed, or
     renumbered);
   - an ADDED section is placed on it (below);
   - its `pd_stale` is set to anything OTHER than a revision strictly above its own
     `pd_curated_rev` (that one shape is a stamp-mismatch release, below): `reconcile-pending`,
     a revision equal to its own stamp (this step's HOLD marker, still present if a run was
     aborted or interrupted before step 5), a revision below it, or a malformed value;
   - its `pd_curated_rev` is missing or malformed.

   A section is one heading's own text, up to the next heading of any level: an edit inside a
   sub-heading amends that sub-heading, not its parent, and a citation to a label that is not
   itself a heading (an item such as `3 P1`) points into the heading section containing it.
   The citation test is MECHANICAL at that granularity — a judgment that the cited clause did
   not really change does NOT make a citing packet unaffected: re-curate it (a re-curation
   that needs no text change is legitimate; report it as such). A section ADDED by a revision
   in range cannot be cited by a packet curated before it, so place each one explicitly:
   every packet whose Objective or Contract it constrains is AFFECTED (and its re-curation
   cites it), else the section goes on the not-decomposed list. Every other packet is
   UNAFFECTED: this step does not touch it — step 5 restamps it.

   Re-curate every affected, UNCLAIMED packet, restamp it (`pd_curated_rev=R`), and HOLD it —
   deferred, with `pd_stale=R` — until step 5. The hold keeps a not-yet-verified re-curation
   out of every consumer's hands (its stamp now matches `pd_rev`, but `pd_stale` is set), and
   because the marker equals its own stamp, a resume after an interruption classifies it
   AFFECTED and verifies it again. An affected packet actively CLAIMED MUST NOT be rewritten
   OR restamped — set `pd_stale=reconcile-pending` on it; the implementer's checkpoints catch
   it, and its old stamp keeps failing the stamp check if it is released and re-claimed,
   until a later reconcile (or catch-up) re-curates it (`reconcile-pending` makes it affected
   there).

4. Re-run the pre-filter + cold-read on re-curated packets and a semantic pass SCOPED to the
   amended sections, the re-curated packets, and their direct planned-ordering neighbors —
   plus, when a revision in range ADDED a section, a coverage check of each added section
   against every not-closed packet's Objective and Contract (confirming step 3's placement).
   The full-set semantic check is for initial release and deliberate audits only. A packet
   step 3 classified unaffected that a finding here forces into a content fix is
   RECLASSIFIED affected: unclaimed ⇒ re-curated, restamped, HELD, and re-cold-read like the
   rest; claimed ⇒ step 3's claimed rule (`pd_stale=reconcile-pending`, no rewrite, no
   restamp). Either way step 5 never restamps it as unaffected. This pass gets the
   severity-gating and 2-round cap too (mode `decompose` step 8's canonical gate-and-cap rule in SKILL.md, by reference —
   not restated here), but reconcile's OWN abort semantics apply, not `decompose`'s: unlike a
   fresh decomposition, the packets here were already released once, so an abort must keep
   them out of the pool actively. An abort at the cap MUST leave every re-curated packet HELD
   (deferred, `pd_stale=<new-rev>`), leave `pd_phase=reconciling:<new-rev>` (so mode `decompose` step 1 (SKILL.md)
   routes it as an interrupted reconcile through its dedup/resume routing), and
   `write-report` (step 6's contents, marked ABORTED) — it MUST NOT run step 5, and MUST NOT
   restore `released` OR `released:partial` on an aborted reconcile; both are
   terminal-release literals and this path is not a release. Unaffected packets therefore
   keep their old stamp and the stamp check keeps refusing them until a resumed reconcile
   completes — correct, since the set's seams are unverified at `R`; a consumer that claims
   one meanwhile takes the stamp-mismatch release below, which the completing run's step 5
   clears. A packet halted by mode `decompose` step 8 (SKILL.md) stays HELD; step 5 skips it and step 6 reports it. Note also
   that this pass's scope is narrower than `decompose`'s (seam consistency only, not full
   coverage), so most findings here will legitimately be `blocking` — severity-gating buys
   this mode fewer skipped rounds than it buys `decompose`.
5. **Restamp the unaffected, release the held** — ONLY after step 4 finished without
   aborting (converged, or released at the cap per mode `decompose` step 8 (SKILL.md)); never earlier, so an aborted or
   interrupted reconcile restamps NO unaffected packet and releases NO held one. Work from a
   FRESH children read, never step 3's: consumers may have claimed, refused, or re-deferred
   packets since. Then:
   - For every UNAFFECTED packet whose `pd_curated_rev` is below `R`, claimed or not: if it
     carries a stamp-mismatch `pd_stale` (a revision strictly above its OLD stamp — a consumer
     refused it), clear `pd_stale` first and undefer it if deferred; then set
     `pd_curated_rev=R` — metadata only, content untouched. On a claimed packet this is the
     same metadata-only kind of write as step 3's `reconcile-pending`: its implementer checks
     the stamp only at claim, so it is unaffected, and a later release and re-claim now
     passes. An unaffected packet whose fresh read shows any OTHER `pd_stale` is not
     restamped: step 6 reports it, and the next reconcile or catch-up classifies it AFFECTED.
   - For every packet this run HELD (re-curated in step 3 or step 4), except one mode `decompose` step 8 (SKILL.md)
     halted: clear `pd_stale` and undefer it.

   The step is idempotent — a packet already at `R` with no `pd_stale` is skipped — so a
   resumed run simply repeats it.

6. **Report, then restore.** `write-report` the reconcile report, headed
   `pd_rev <R-1> -> <R>`: the design sections revision `R` amended and any it ADDED (a later
   reconcile's step 3 reads this for packets stamped below `R`); every not-closed packet's
   outcome — re-curated (noting any with no text change, with its per-packet fixed-read
   estimate as in mode `decompose` step 10 (SKILL.md)), marked `reconcile-pending` (claimed), halted,
   restamped unaffected (`<old>` → `R`, plus any `pd_stale` cleared and packet undeferred),
   or left unrestamped (step 5's other-`pd_stale` case); and the not-decomposed list. Only then
   restore `released` or `released:partial` (per step 2).

**Stamp catch-up (no amendment)** — the repair for a docket whose packets were left behind
at an old stamp: every docket reconciled before step 5 existed (its unaffected packets kept
their pre-reconcile stamp, so the stamp check refuses them forever and each refusal comments
"reconcile owed"), and any docket holding a `reconcile-pending` packet whose claim has since
ended. Run it ONLY on a docket at `released`, `released:partial`, or `reconciling:<R>` — never
mid-decomposition (`curating`, `failed:<phase>`, and the like belong to mode `decompose`'s
RESUME). Run steps 3–6 with NO `amend-design`: skip steps 1–2 (no pre-check, no `pd_rev`
bump), take `R` = the docket's current `pd_rev`, and set `pd_phase=reconciling:<R>` for the
run. Step 3's multi-revision test reads each earlier revision's amended sections from the run
that bumped `pd_rev` to it, or from a diff of the recoverable design texts. Head step 6's
report `catch-up at <R>, no amendment` — it is never the amended-sections record for any
revision. This is the remedy whenever a stamp-mismatch
release lands on a docket whose last reconcile completed.

**Resuming `reconciling:<R>`** (an aborted or interrupted reconcile): with no new amendment,
run the catch-up shape at `R` — never step 2 again for an amendment already committed; with a
NEW amendment, run the full mode from step 1, bumping to `R`+1 (step 3's multi-revision test
then covers revision `R` too).

**Stamp-mismatch releases** (a consumer found rotted curation first): the release MUST
re-DEFER the packet and set `pd_stale=<found-rev>` — never return it to the open pool, or
every queue consumer claims/checks/releases it in an endless cross-session spin. It MUST NOT
overwrite a `pd_stale` already set: `reconcile-pending` and the HOLD marker are what step 3
reads to know the packet still needs re-curation. A completing RECONCILE clears `pd_stale`
and undefers, in its step 5 — after re-curation (step 3) for an affected packet, directly for
an unaffected one; the docket report channel tells the operator a reconcile is owed.
