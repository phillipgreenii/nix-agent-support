# Invariants — pa-monitor

Rules this set's implementation MUST hold, following the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`). Names are namespaced by topic
(`INV-3`) since this set cites the method.

## Scope (`INV-SCOPE-*`)

- **`INV-SCOPE-1`** <!-- uuid: 12ec04ac-8a97-4a42-970f-5b35d6eecfd0 --> — **Session status and
  account usage are two never-conflated scopes.** A per-session surface (status, blocker) MUST
  NOT carry an account-scoped number, and a surface reporting an account usage window MUST NOT
  carry or imply a per-session identity. No per-session usage percentage exists anywhere in this
  set's surfaces; usage is inherently account-level.

## Session status (`INV-STATUS-*`)

- **`INV-STATUS-1`** <!-- uuid: fabf35f4-0f7c-473f-b4f1-383eade76c3a --> — A monitored session's
  status is exactly one of **working**, **blocked**, or **idle**. When, and only when, status is
  **blocked**, a **blocker** MUST also be reported, and it MUST be exactly one of **human input**,
  **human authentication**, **usage limit**, or **error**. A **usage-limit** blocker MUST be
  derived from that session's own signals alone; it MUST NOT be derived from, or imply anything
  about, the account-level usage window (`INV-SCOPE-1`).

## Usage windows (`INV-WINDOW-*`)

- **`INV-WINDOW-1`** <!-- uuid: 7dd3a1b5-9f2f-41a0-8c54-d62ec3a85146 --> — A reported usage-window
  percentage is the **peak** reading observed for the **current** window, never merely the newest
  reading. The peak resets only when a **new** window begins (a later window-reset instant is
  observed); within one window the reported value MUST NOT fall as a result of a lower
  intermediate reading arriving.
- **`INV-WINDOW-2`** <!-- uuid: e54a38bb-332b-419a-b487-3595131dc438 --> — The published capture
  time for a usage reading means **reading-stream liveness** — the instant any usage signal was
  last observed at all — and MUST NOT be presented as, or confused with, the instant the reported
  peak itself was captured. A consumer using capture time to judge staleness (`INV-STALE-1`) is
  judging "is the pipeline alive", not "is the peak fresh".
- **`INV-WINDOW-3`** <!-- uuid: 37da5fb7-d5d3-4df4-9732-60ebde317b86 --> — **Unknown and zero MUST
  be distinguishable at every layer a usage reading passes through.** An unknown reading MUST NOT
  surface as a 0% value and MUST NOT surface as an unset/epoch timestamp indistinguishable from "no
  time has passed". A layer receiving an unknown reading MUST NOT overwrite a previously known
  value with it; a layer receiving a **known-absent** fact (e.g. "no session is currently blocked
  on a usage limit") MUST overwrite a stale prior value — unknown and known-absent are different
  facts and propagate differently.

## Actuation (`INV-ACT-*`, `INV-NUDGE-*`, `INV-AWAKE-*`)

- **`INV-ACT-1`** <!-- uuid: d6c8423f-750c-49be-a47c-4a85bb4808b9 --> — **pa-monitor is not
  read-only.** It never writes into the transcripts or state it observes, but it deliberately
  **injects input into monitored sessions** (a nudge, `INTF-NUDGE`) and **holds a power assertion**
  that changes whether the machine is allowed to sleep (`INV-AWAKE-1`). Both MUST be documented as
  **owned actuation**, never described as, or reduced to, observation.
- **`INV-NUDGE-1`** <!-- uuid: 78b52931-d636-489d-8ce8-42ea8d9e461b --> — A nudge MUST be
  **suppressed** — never delivered — under any of: the target session's status is **working**
  (nothing to nudge); the target session is **blocked** with a **human** blocker (a nudge cannot
  answer a question only a person can answer); or the target session carries the no-nudge opt-out
  marker of `INTF-NONUDGE`. A nudge intent's own eligibility condition (its window/limit/disruption
  having actually lifted, or an explicit operator request) MUST be satisfied in addition to, never
  instead of, these suppression rules.
- **`INV-AWAKE-1`** <!-- uuid: a9ad9b72-9d8d-4c25-a578-44d98d29e2d9 --> — The daemon MUST hold the
  machine awake while any monitored session is **working**, or is **blocked** in a way the machine
  itself can resolve without a person (a usage limit, or a retryable error awaiting automatic
  recovery). The daemon MUST let the machine sleep when the only pending work is a session
  **blocked on a person** — sleeping does not delay a human who is not currently looking, and a
  session that only a person can unblock gains nothing from a machine held artificially awake. The
  power cost of holding a machine awake for a full usage window is an accepted consequence of this
  rule, not a defect.

## Gates (`INV-GATE-*`)

- **`INV-GATE-1`** <!-- uuid: 05fd471b-98ee-4ecd-8408-5016dc074c04 --> — The **busy** predicate is
  true if and only if at least one monitored session is **working**; a **blocked** session MUST
  count as **not busy**. Consequently "idle reached" (no session working for a sustained
  observation period) MUST NOT be read as "all pending work is finished" — a caller for whom that
  distinction matters MUST additionally consult the blocked count rather than the busy/idle result
  alone. This is a declared semantics (`ACTOR-PAM-CALLER`'s obligation), not a defect to be fixed
  by redefining busy.

## Staleness (`INV-STALE-*`)

- **`INV-STALE-1`** <!-- uuid: 8d9059b5-eaea-4d91-9c6f-a3ba65a03e6e --> — The daemon MUST publish
  the raw capture time (`INV-WINDOW-2`) alongside every usage reading it serves, so a consumer CAN
  judge staleness for itself. The daemon is NOT required to publish a staleness verdict or a
  staleness bound of its own; where it does not, each consumer necessarily judges staleness by its
  own tolerance, and two consumers with different tolerances MAY disagree about the same reading —
  this divergence is a realization gap, tracked in `## Realization gaps`, not a contradiction in
  this invariant.

## Selector resolution (`INV-SELECT-*`)

- **`INV-SELECT-1`** <!-- uuid: f55a4d1b-339a-46c2-99d8-bcf17ca886c0 --> — A session selector MUST
  resolve to **exactly one** monitored session before any action naming it proceeds. A selector
  matching **no** session and a selector matching **more than one** are both reported as such;
  neither is silently treated as a match, and the daemon MUST NOT guess among several matches.

## Vocabulary (`INV-VOCAB-*`)

- **`INV-VOCAB-1`** <!-- uuid: b434a3fe-7fb1-4a0e-9d92-1bb48e36c7a3 --> — This set's glossary MUST
  disambiguate every pair of look-alike usage-adjacent quantities it defines — at minimum: the
  account-level window reset vs. a session's own recoverable-block reset; the window percentage
  (`INV-WINDOW-1`) vs. any other ratio a surface might report. A new quantity resembling an
  existing one MUST be added to the glossary with an explicit contrast to what it is not, per
  `INV-14`'s named-concept rule.
