# pg-desk — `run issue` (beads backend)

`pg-desk run issue <id> --change added|changed|removed|sweep` (Phase 10, docket pg2-2j5ac.34) is
the beads-backend half of the `run`'s own design-level rule (see
[`pipeline-run.md`](pipeline-run.md)): an issue-type entity change re-runs stage 2 (interpret) for
the PR it is about, without a fresh gather and without re-running sync. This replaces `pg-pr
changes`'s beads-closing re-interpretation trigger.

## What triggers it

A beads-tracker issue-type entity — an anchor, feedback cycle, or review-request bead (see
[`sync.md`](sync.md)'s "Bead shapes" table) — closing, changing, or being added/swept.
`--change`'s four values (`added`/`changed`/`removed`/`sweep`) are accepted identically: none of
them changes this command's own dispatch. The flag exists only so the caller (the ZR desk-issue
role, and the event source's own item metadata) can still log or route on it.

## Resolution (bead -> PR)

Given the triggering bead's id, `run issue`:

1. Reads the bead's CURRENT title and metadata via `pg-connector issue show <id>` — never through
   `internal/gather.Gatherer.Gather`, which rejects any entity type other than `pr` outright (see
   [`gather.md`](gather.md)).
2. Classifies that title/metadata with the SAME bead-shape parser [`sync.md`](sync.md)'s adoption
   step uses for its own forward direction (PR -> bead) — one parser, not two. An anchor or
   review-request bead resolves by its `repo`/`pr_number` metadata; a feedback cycle resolves the
   same way, or by its exact `process-feedback: <repo>#<n>` title when a pre-existing cycle
   predates that metadata; a title-adopted merge-request bead (titled `<repo>#<n>: ...`, no
   `repo`/`pr_number` metadata yet) resolves by its title prefix.
3. Re-runs interpret for the resolved PR's entity id, against whatever facts the store already has
   for it (`entity` table, last written by an earlier `pr`-triggered `run`) — never a fresh
   gather, and never the sync stage: this path only re-derives interpretation from data that is
   already current, on the theory that the linked issue changed, not the PR itself.

A bead whose title and metadata match none of the known shapes (nor the title-adopted fallback) is
a clear, non-panicking error — `run issue` does not guess.

## Exit codes

Same contract as every other `run` invocation (see [`pipeline-run.md`](pipeline-run.md)): exit `0`
on success or on a degraded re-interpretation, exit `1` when the bead cannot be read or the store
cannot be read/written, and MUST NEVER exit `9` or return a raw `pg-connector` exit code.

## Telemetry and logs (D24)

`run issue` emits nothing over OpenTelemetry or Prometheus through Phase 10 (export is a later
observability item, alongside pg-router's own metrics sink). Its outcome folds into `run`'s
existing structured JSON log line to stderr — logged against the RESOLVED PR's own entity id (not
the triggering bead's id), with `change` carrying whatever `--change` value was passed, exactly as
the `pr` path logs. `--verbose` prints a single `interpret` timeline entry (no `gather` or `store`
entries — this path never gathers, and its `store` write is folded into the same structured
outcome the `pr` path uses).

## Out of scope

`run issue` for Jira and `run thread` are Phase 13, together with the cross-reference step that
would give either kind of event a linked PR. ZR's desk-issue role wiring (the caller that invokes
this command) is this docket's sibling packet. `daemon.enable` is this docket's last packet.
