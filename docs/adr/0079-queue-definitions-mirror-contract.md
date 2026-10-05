# Queue definitions: a machine-readable mirror, checked for drift and classified at load

**Status**: Accepted
**Date**: 2026-10-05
**Deciders**: phillipg

## Context

The `pb` plugin's drain and unblock commands each pull work from named queues, every one a
`bd ready` invocation with a particular flag list. The command markdown is what agents read, so it
is the canonical statement of what each queue selects. The beads exporter (ADR 0078) must report
how many beads each queue currently offers, and therefore needs the same flag lists in a form a
program can read.

Two things could go wrong. If the exporter keeps its own copy of the queue definitions, the copy
drifts from what agents actually run, and the dashboard reports a queue nobody pulls from. And if
the exporter runs arbitrary flag lists it was handed, a typo or a hostile edit could pass a
write flag (such as a claim) to bd from a monitoring daemon.

## Decision

1. **`claude-marketplace/pb/queues.json` is a mirror, not a source.** It is a JSON array of
   exactly four objects, each with a `name` and an `args` array (the tokenised flag list that
   follows `bd ready`), and no other fields. The command markdown stays canonical.
2. **Drift is detected, not generated away.** Each canonical query in the markdown carries a
   `pb-queue` marker comment above its fenced command, and the `test-pb-queues-mirror` check
   fails when the markdown and the JSON disagree. Generating one from the other at build time is
   rejected by ADR 0017 for static marketplace content, so the mirror is kept honest by a check
   with mutation tests instead.
3. **The exporter's configuration carries the queues.** The module that renders the exporter's
   configuration reads the mirror and writes the queues into the configuration file; the exporter
   never reads the mirror directly at runtime. Its own Go tests read the real file so a change to
   it that the exporter cannot handle fails the exporter's tests.
4. **Each queue is classified at configuration load, and the class decides how it is evaluated.**
   - _client-side_: only `--label` (the bead has all the labels), `--exclude-label` (the bead has
     none of them) and `--exclude-type`. The queue is filtered in process from the one shared
     `bd ready` result, and beads marked as templates are dropped.
   - _spawn-only_: any other flag from a small allowlist of read-only filters (priority, parent,
     type, assignee, unassigned, any-of labels). The exporter spawns `bd ready` with the queue's
     flags for it.
   - _rejected_: anything else, including every write flag, every unknown flag, a positional
     argument and a flag missing its value. Loading the configuration fails, and the same check
     runs inside the nix build sandbox, so a bad queue never reaches a running daemon.

## Consequences

### Positive

- The dashboard's queue numbers are, by construction, the queues agents actually pull from, and
  the day they diverge a check goes red.
- A configuration error cannot turn the monitor into a writer.
- All four current queues are client-side, so queues cost no extra bd spawns.

### Negative

- Adding a queue means editing the markdown, the mirror and (for a spawn-only queue) possibly the
  allowlist, in that order. The drift check makes a missed step visible but not automatic.
- The allowlist is deliberately small; a queue that needs a flag outside it requires a code
  change, which is the point.

## Alternatives Considered

### Generate the mirror from the markdown at build time

Rejected for the reason given in ADR 0017: static marketplace content is not built from
substituted inputs.

### Have the exporter read the markdown

Rejected. Parsing prose for fenced commands at runtime couples the daemon to documentation
formatting; the marker-based check does that parsing once, in the build.

### Spawn `bd ready` for every queue

Rejected in ADR 0078: cost would scale with the number of queues.

## Related Decisions

- [0078](0078-beads-content-exporter-ownership.md): the exporter that consumes the queues.
- [0017](0017-static-nix-built-local-plugin-marketplace.md): the no-substitution rule.
