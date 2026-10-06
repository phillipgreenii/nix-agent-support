# Actors — work-report

Who interacts with work-report. Everything it integrates with is an actor — human or system; an
**interface** is _how_ an actor interacts. A behavior docs set MUST define all of its actors
(`INV-13`).

## Principal (human or agent)

- **`ACTOR-OP` — Operator** <!-- uuid: e9af128c-1447-480e-a076-0eadd18bc1dd --> — a **principal —
  a human or an agent** — that configures sources and triggers a pull for a range (ingestion), and
  that requests a report for a range, in a kind, optionally narrowed (reporting). One operator,
  the same across both halves; work-report has no notion of an ingestion-side operator distinct
  from a reporting-side one. Works through `INTF-CONTROL` and `INTF-REQUEST`, both drivable by
  either a human or an agent.

## System actors (participants behind interfaces)

Each interacts with work-report only through its interface; which concrete tool fills the role is
this repository's own deployment concern.

### Ingestion

- **`ACTOR-SOURCE` — Source** <!-- uuid: 623dbdaf-7cd3-45fd-b782-fcc4382e9296 --> — a system that
  holds a record of things the operator did or facts about something external, and can answer a
  pull request for a range with entries (`INTF-INGEST`). git, GitHub, Claude Code sessions,
  beads, and Jira are today's instances of this role; a new instance MAY be added without changing
  an existing one's behavior (`INV-PLUGIN-1`). A source decides its own strategy for satisfying
  repeated or overlapping pulls efficiently — that decision is entirely its own (`INV-RANGE-1`) —
  and carries no obligation to remain reachable; its unavailability is an ordinary, expected pull
  outcome (`glossary.md` "Pull outcome").
- **`ACTOR-READER` — Reader** <!-- uuid: de223db6-7231-4c30-b1e4-8cd97e724022 --> — any consumer
  that queries the store through `INTF-READ`. work-report's own reporting half is one instance of
  this role — it queries through exactly the same boundary any other reader would — and nothing
  distinguishes one reader from another at this interface.

### Reporting

- **`ACTOR-GENERATOR` — Report generator** <!-- uuid: f89661d6-350f-4340-9bfd-d497cd696ecb --> —
  the implementer of `INTF-GENERATE`: given queried entries and any narrowing, it renders a report
  for a non-baseline kind. Its rendering mechanism is unspecified and out of this set's floor — a
  new generator MAY be added without changing any existing kind's request or response shape
  (`INV-GEN-PLUGIN-1`). The baseline kind (`glossary.md` "Kind") requires no generator at all
  (`INV-BASELINE-1`).
