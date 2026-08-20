# Glossary — pa-monitor

Vocabulary for pa-monitor's CLI and daemon. cmux-bridge and the TUI are named boundaries
(`actors.md`); their internal vocabulary is out of this set's extent.

## The two scopes (never conflated — `INV-SCOPE-1`)

- **Session status** — one of exactly three values a monitored session is in: **working**
  (a turn is in flight), **blocked** (progress is halted on something outside the session's own
  control), or **idle** (nothing in flight and nothing blocking). Per-session.
- **Blocker** — when a session is blocked, the reason: **human input** (the session is waiting on
  a person to answer something), **human authentication** (a person must re-authenticate), a
  **usage limit** (the session hit a provider usage window and will resume on its own), or an
  **error** (an unresolved failure). Empty when the session is not blocked.
- **Account usage window** — the provider's own rolling accounting of consumption against a plan
  limit, at two durations (a short window and a long window). Account-scoped: it describes the
  operator's account, never one session, and it is never derived from — or presented as — a
  per-session reading.
- **Window percentage** — how much of the current account usage window has been consumed, as the
  **peak** reading observed for that window (`INV-WINDOW-1`), not the newest reading.
- **Capture time** — the instant the daemon last read any usage signal at all (`INV-WINDOW-2`).
  It answers "is the reading pipeline alive", never "when was the peak observed" — those are
  different questions and MUST NOT be conflated.
- **Window reset** — the instant an account usage window lifts and its consumption clears. Distinct
  from a **session's own recoverable-block reset** (below) — an account-level fact vs. a
  per-session one (`INV-VOCAB-1`).
- **Session's own recoverable-block reset** — the instant a specific usage-limit-blocked session
  becomes eligible to resume, folded up across sessions as the **earliest** such instant the daemon
  currently knows of. Not persisted between daemon restarts; a live-only reading.

## Actuation (pa-monitor is not read-only — `INV-ACT-1`)

- **Nudge** — text the daemon injects into a monitored session's input, followed by a submit
  action, so the session resumes progressing on its own. An **owned actuation**: the daemon
  changes what is running, not merely what it observes.
- **Nudge intent** — why a nudge fires: the account window lifted (**window-reset**), a session's
  own usage-limit block lifted with no lift the daemon can otherwise detect
  (**limit-pause**), the session recovered from a disruption (**disrupted**), or the operator asked
  for it directly (**manual**).
- **Suppression** — a nudge intent's obligation to withhold delivery when it would be wrong: never
  to a working session (nothing to nudge), never to a session blocked on a person (a nudge cannot
  answer a question a nudge did not ask), and never to a session opted out via the no-nudge
  contract (`INTF-NONUDGE`).
- **Keep-awake** — the daemon's power assertion: while any monitored session is working, or
  blocked in a way the machine itself can resolve (a usage limit, a retryable error), the machine
  is held awake so that work is not lost to a sleep the operator never asked for. A session blocked
  on a person is let sleep — no amount of wakefulness helps a session waiting on a human
  (`INV-AWAKE-1`).

## The gates

- **Busy** — at least one monitored session is `working`. A **blocked** session is explicitly NOT
  busy (`INV-GATE-1`) — this is a declared semantics, not an oversight.
- **Idle reached** — the busy predicate has read false for a sustained period. It answers "is
  anything actively progressing", not "is all pending work finished" — a caller that needs the
  latter MUST also read the blocked count.

## Staleness

- **Staleness verdict** — a consumer's own judgement, made by comparing the published capture time
  against its own tolerance, that a reading is too old to act on. pa-monitor publishes the raw
  capture time; it does not itself publish a verdict or a bound (`INV-STALE-1`).

## Boundaries

- **cmux-bridge** — a relay that runs inside a multiplexer pane the daemon cannot reach directly,
  registering, heartbeating, and carrying out actuation the daemon instructs, then reporting the
  outcome back (`INTF-BRIDGE`).
- **The TUI** — an interactive human client of the state-read surface (`INTF-STATE`); it renders
  what it reads and issues no actuation of its own beyond what any client may.
