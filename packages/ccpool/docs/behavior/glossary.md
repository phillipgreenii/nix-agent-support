# Glossary — ccpool

Vocabulary for ccpool's session pool. The agent binary's own internal behavior, and any
deployment-specific workflow built on top of ccpool, are out of this set's extent.

## Pool

- **Pool** — a named collection of sessions, isolated as a unit: its own identity, its own
  substrate, its own registry entry, its own reap policy. A pool **is** a directory — its identity
  and its runtime substrate both derive from that one canonical location, so two pools never share
  a substrate by accident.
- **Registry** — the machine-wide record of every pool that has ever run, used by the periodic
  sweep to find pools to reap even when nothing is actively dispatching to them.
- **Timer-driven sweep** — a periodic pass, run independently of any dispatch, that reaps every
  registered pool per its own policy.

## Sessions and the two state vocabularies

- **Session** — one long-lived run of the agent, addressed by the caller's own chosen name and
  tracked across many prompts.
- **Store state** — the last **observed turn outcome**, a **fact** recorded when the agent
  signals a transition. It is a cache of history, not a live read.
- **Reconciled state** — what the session is doing **right now**, derived on read by combining
  liveness with observation. It is never persisted; asking again may answer differently even if
  nothing was ever recorded.
- **The six store states** — `starting` (launch under way), `ready` (accepted for input, nothing
  dispatched yet), `working` (a turn is in flight), `needs_input` (alive, non-terminal, holding a
  question), `idle` (the last turn **ended**), `errored` (the last turn **failed**).
- **`needs_input` is not a failure** — it is a **live non-terminal state**: the session is alive,
  nothing is lost, and it resumes exactly where it left off once answered.
- **Failure reason** — the classification and diagnostic text a consumer would need to tell a
  retry-worthy failure from a fatal one. Declared intent; not yet a field on the status surface
  (`## Realization gaps`).
- **Liveness** — whether the session's underlying process is actually running. Always derived on
  read from the runtime substrate, never cached in the store.

## Dispatch

- **Dispatch** — send a prompt to a session and, depending on mode, either wait for the outcome or
  return immediately.
- **Busy refusal** — the default response to a dispatch aimed at a session already `working`: the
  caller is told busy rather than queued or silently dropped.
- **Fire-and-forget** — a dispatch mode that returns immediately without waiting for the turn's
  outcome; the session's status is read later, separately.
- **Ingestion confirmation** — an optional check, for fire-and-forget delivery, that the prompt was
  actually taken up by the agent (a first response observed) rather than lost before it was ever
  seen.

## Autonomous mode

- **Autonomous mode** — a per-dispatch mode in which the session's human-question channel is
  **structurally removed**: a question the agent tries to ask is denied before it is asked, with a
  reason, rather than left waiting for nobody. Caller-owned per dispatch; it does not survive a
  relaunch that does not restate it.
- **Question denial** — the reasoned refusal the agent inside the session receives when it
  attempts to ask a question under autonomous mode (`INTF-DENY`). Distinct from `needs_input`:
  denial happens instead of the question ever being posed; the session that denies a question can
  still separately enter `needs_input` for reasons unrelated to that one denied attempt.

## Metadata

- **Session metadata** — an opaque, caller-owned key/value set carried on a session. ccpool never
  interprets a key's meaning; it only stores, returns, and lets a caller filter by it. Identity
  metadata (who owns this session, for whose purpose) is the caller's convention, not ccpool's.

## Isolation and consent

- **Folder trust** — the agent's own per-directory trust gate; ccpool pre-establishes it for a
  session's directory before launch so the session never hangs on an interactive trust prompt the
  caller cannot see.
- **Unclassified-tool pre-denial** — ccpool's deny-by-default posture toward external tool servers
  a session's directory declares but the deployment has not classified: pre-denied before launch,
  never left to an interactive prompt.

## Retry and the meter

- **Transient failure** — a turn failure ccpool's own runtime can already tell apart from a
  rate-limited or terminal one, sufficient to retry the turn in place, bounded by attempts and a
  time window.
- **Consumption meter** — a running read of a session's own resource use. Declared intent (ccpool
  owns the meter, D12); not yet a surface ccpool serves (`## Realization gaps`).

## Retention and reaping

- **Reap** — periodic reclamation: sessions past an inactivity bound, and the least-recently-active
  sessions once the pool is over its cap, are closed and their records reclaimed.
- **Human-awaited session** — a session in `needs_input`. Reap MUST spare it, TTL and cap eviction
  alike, even if that leaves the pool briefly over its cap.

## Notification

- **Notifier** — an adapter ccpool hands an event to on a notifying transition; ccpool emits the
  event and does not own delivery to the operator.
