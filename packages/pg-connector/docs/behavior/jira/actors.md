# Actors — pg-connector-issue-jira

Who and what interacts with the daemon-backed Jira backend. Everything the backend integrates with
is an actor, human or system; an **interface** is _how_ an actor interacts. A behavior docs set MUST
define all of its actors (method `INV-13`). The parent set's `ACTOR-BACKEND` is the role this
backend plays toward the umbrella; the actors below are the parties on the other sides of this
backend's own boundaries.

## Principals (human or agent)

- **`ACTOR-JIRA-CALLER` — Caller** <!-- uuid: f15e01a9-7452-4f6c-8a3b-40f8960dec87 --> — a **principal, a human or an agent** that
  reads issues and writes to them (creates, comments, transitions, updates, closes), through the
  umbrella. An operator at a terminal, a skill, a sync job, a rescue tool and a decision role are all
  callers, and the backend draws no distinction between them. A caller's write is executed by its own
  process against the origin tool, under the caller's own identity, and never needs the daemon. A
  caller MAY ask, with the watch option on one read, that one issue be watched. Works through
  `INTF-JIRA-WIRE`.
- **`ACTOR-JIRA-CONSUMER` — Change consumer** <!-- uuid: c6dbc122-619a-4230-9e2f-5a32bbbece58 --> — an automation that polls what
  changed on its own clock, acts on the changes, and acknowledges what it has received. A scheduler
  role is one; so is a script. It identifies itself by a stable consumer name, holds one position in
  the daemon's change log under that name, and works through `INTF-JIRA-WIRE`'s `changes` and
  `changes_ack`. It keeps no copy of the issue data it is told about: after a change it reads the
  issue back, and that read is a local hit.
- **`ACTOR-JIRA-OPERATOR` — Operator** <!-- uuid: 1557fc36-d549-49a4-9686-082e0b0e47de --> — a **principal, a human or an agent**
  that asks whether the daemon is healthy, how fresh its data is, how many issues it tracks and
  watches and how much it is spending, asks why one issue is stale or silent, and writes the
  configuration the daemon and its client read, including which queries are watched. Works through
  `INTF-JIRA-STATUS` and `INTF-JIRA-CONFIG`. The same person or agent MAY also be a caller or a
  consumer; the role is what this set tells apart.

## System actors (participants behind interfaces)

- **`ACTOR-JIRA-UMBRELLA` — Umbrella** <!-- uuid: 4db32092-bfcf-40bc-bbaf-16203f26c8ac --> — `pg-connector`, the Facade that
  executes the backend once per call, forwards `changes` and `changes_ack`, and acknowledges only
  after it has flushed its output. Its half of the seam is the parent set's, and this backend
  implements that contract (`INTF-WIRE`). Interface: `INTF-JIRA-WIRE`. Essential: the backend has no
  caller, and no consumer, without it.
- **`ACTOR-JIRA-ORIGIN` — Origin** <!-- uuid: b91be634-b6ff-4c9a-9b91-d2dee83fa645 --> — Jira, the upstream system that holds the
  truth about issues, is shared with every other user of the same tenant, limits the rate of
  requests it accepts, and may answer slowly, wrongly or not at all. The
  daemon and the client reach it only through the origin tool (`pjira`), which holds the
  credential. Interface: `INTF-JIRA-ORIGIN`. Essential.
- **`ACTOR-JIRA-SUPERVISOR` — Supervisor** <!-- uuid: e778c0ce-297e-4f77-96b3-175b27ef157a --> — the per-platform process manager
  that starts the daemon at login and starts it again when it exits. It has no logic of its own and
  no knowledge of the backend. Interface: `INTF-JIRA-SUPERVISION`. Optional: the daemon runs when an
  operator starts it by hand, and a deployment on a platform with no supervisor is valid.
- **`ACTOR-JIRA-MONITOR` — Monitor** <!-- uuid: c6c538fc-8668-4678-9743-416d592b2428 --> — the observability stack that collects the
  daemon's metrics and event log, evaluates the alert rules over them, and tells the operator when
  one fires. It has no logic of the backend's own and holds no state the daemon depends on.
  Interface: `INTF-JIRA-TELEMETRY`. Optional: the daemon works with no monitor, but the outcomes it
  is meant to make visible are then invisible.
