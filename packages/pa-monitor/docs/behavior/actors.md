# Actors — pa-monitor

Who interacts with the daemon. Everything the daemon integrates with is an actor — human or
system; an **interface** is _how_ an actor interacts. A behavior docs set MUST define all of its
actors.

## Principals (human or agent)

- **`ACTOR-PAM-OP` — Operator** <!-- uuid: 55c4b056-89d5-451d-aa57-f79d301b550b --> — a **principal
  — a human or an agent** — that reads the daemon's state (status, usage, blockers), toggles
  keep-awake and auto-resume, and nudges a stuck session. Works through the state-read surface
  (`INTF-STATE`) and the actuation surface (`INTF-NUDGE`), both drivable by either a human or an
  agent.
- **`ACTOR-PAM-CALLER` — Gate caller** <!-- uuid: 35392c2e-1f7e-4b83-a717-f7f465d51c64 --> — a
  script or another program that blocks on a busy/idle gate before proceeding (`INTF-STATE`,
  `INV-GATE-1`). A specialization of a driving-port consumer, named separately because its
  obligations on reading the gate result are distinct from an interactive operator's
  (`packages/pa-monitor/README.md` "Busy/idle gates").

## System actors (participants behind interfaces)

Each interacts with the daemon only through its interface; which concrete tool fills the role is
this repository's own deployment concern, not a downstream one — pa-monitor is deployed directly,
with no separate adapter set.

- **`ACTOR-PAM-SESSION` — Monitored agent session** <!-- uuid: 84f830eb-2577-4d93-9589-32c3eb310a65
  --> — an agent session the daemon observes and, when eligible, **acts upon**: the daemon derives
  its status and blocker from the session's own signals and MAY inject a nudge into it
  (`INTF-NUDGE`). The session does not register with the daemon and carries no obligations of its
  own — it is discovered and observed, never a willing participant in a protocol.
- **`ACTOR-PAM-COACTOR` — Co-resident actuator** <!-- uuid: 66a1f70d-272e-4359-b28f-27933bfa99c4
  --> — another tool that also drives input into the same monitored sessions (e.g. an
  agent-session-pool manager). It is the counterparty on the no-nudge opt-out contract
  (`INTF-NONUDGE`): it marks a session excluded from this daemon's nudging so the two tools do not
  double-actuate the same session.

## Boundary counterparties (named, internals undescribed)

Per this set's floor (`INV-13`), these two are named as **boundaries** the daemon hands work
across — not as system actors with their own catalog, because their internals are deliberately out
of this set's extent.

- **cmux-bridge** — a process that runs inside a multiplexer pane the daemon itself cannot reach,
  and relays actuation on the daemon's behalf there (`INTF-BRIDGE`).
- **The TUI** — an interactive client of the state-read surface (`INTF-STATE`); one consumer among
  others, named because it is the daemon's primary human-facing client.
