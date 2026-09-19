# pg-desk — `run issue` (beads backend and Jira)

`pg-desk run issue <id> --change added|changed|removed|sweep` (Phase 10, docket pg2-2j5ac.34, for
the beads backend; Phase 13, docket `pg2-2j5ac.40`, for Jira) is the design-level rule (see
[`pipeline-run.md`](pipeline-run.md)): an issue-type entity change re-runs stage 2 (interpret) for
the PR(s) it is about, without a fresh gather and without re-running sync. This replaces `pg-pr
changes`'s beads-closing re-interpretation trigger, and its Jira half is the Jira-side
counterpart of the same trigger.

## Which backend: dispatch by shape

The incoming id's own SHAPE decides which resolution below applies — checked FIRST, and dispatch
branches directly to the matching path; `run issue` never tries one resolution and falls back to
the other. An id that matches the configured `ticket_patterns` list, anchored to the WHOLE
string (`^<pattern>$`, not a substring search), is Jira-shaped; anything else is treated as a
beads id. This is the SAME `ticket_patterns` list [`gather.md`](gather.md)'s ticket-key scan uses
— one config-driven mechanism, never two independently invented patterns. An unconfigured (empty)
`ticket_patterns` means no id is ever recognized as Jira-shaped, so every id falls through to the
beads path unchanged — a safe default for a Phase-13-unaware deployment, not a regression. An id
matching neither shape is this dispatch's own well-formed error, never a silent no-op.

## What triggers it

**Beads backend:** a beads-tracker issue-type entity — an anchor, feedback cycle, or
review-request bead (see [`sync.md`](sync.md)'s "Bead shapes" table) — closing, changing, or being
added/swept.

**Jira:** a Jira issue already cross-referenced to one or more PRs (see
[`gather.md`](gather.md)'s ticket-key scan) changing, closing, or being added/swept — typically via
the ZR desk-issue role's own `issue-jira-mine` feed wiring (this docket's sibling packet), which is
what actually triggers this in the live system.

`--change`'s four values (`added`/`changed`/`removed`/`sweep`) are accepted identically on EITHER
path: none of them changes this command's own dispatch. The flag exists only so the caller can
still log or route on it.

## Resolution (Jira issue -> PR(s))

For a Jira-shaped id, `run issue`:

1. Looks up every PR currently cross-referenced to it (the `xref` table's reverse, to-id-keyed
   lookup — every row for this ticket key, regardless of which PR it came from).
2. Re-runs interpret (stage 2 only — never gather, never sync) for EACH linked PR, against
   whatever facts the store already has for it. A PR's own cross-referenced Jira issue data
   (fetched by [`gather.md`](gather.md)'s ticket-key scan, stored alongside its other facts) is
   read from that SAME stored data — never a fresh `issue show` — so the layered urgency signal
   (see [`interpret.md`](interpret.md)) reflects whatever Jira state gather last observed, not
   necessarily this triggering event's own payload.
3. Writes zero beads, on every linked PR, exactly like the beads path.

A ticket key with no currently cross-referenced PR at all (nothing has linked one yet) is a
no-op — exit `0`, nothing to re-interpret — mirroring `--change removed`'s own "an id the store
does not know is a no-op" convention. If more than one PR is linked, every one is attempted; a
failure re-interpreting one does not skip the rest.

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
on success, on a degraded re-interpretation, or on either backend's own no-op case (an
unresolvable-yet bead reference is still an error; a Jira id with no linked PR is not); exit `1`
when a bead cannot be read, a Jira id's xref lookup or a linked PR's re-interpretation fails, or
the store cannot be read/written; MUST NEVER exit `9` or return a raw `pg-connector` exit code.

## Telemetry and logs (D24)

`run issue` emits nothing over OpenTelemetry or Prometheus through Phase 13 (export is a later
observability item, resolved by the observability review `pg2-7kizi`, alongside pg-router's own
metrics sink). On the beads path, its outcome folds into `run`'s existing structured JSON log line
to stderr — logged against the RESOLVED PR's own entity id (not the triggering bead's id), with
`change` carrying whatever `--change` value was passed, exactly as the `pr` path logs; `--verbose`
prints a single `interpret` timeline entry (no `gather` or `store` entries — this path never
gathers, and its `store` write is folded into the same structured outcome the `pr` path uses). On
the Jira path, the SAME structured log line is emitted once PER linked PR re-interpreted (each
logged against that PR's own entity id) — a Jira ticket key linked to N PRs produces N log lines,
never one aggregate line, since `run`'s own structured-log shape is per-entity by construction.

## Out of scope

`run thread` and its own cross-reference step (permalinks and ticket keys found in Slack thread
text) are the Slack half — see [`run-thread.md`](run-thread.md). The project-health half of
layered urgency stays deferred (see [`interpret.md`](interpret.md)). ZR's desk-issue and
desk-thread role wiring (the callers that invoke this command) is this docket's own ZR-wiring
sibling packet. `daemon.enable` is this docket's last packet.
