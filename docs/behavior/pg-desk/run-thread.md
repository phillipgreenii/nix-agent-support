# pg-desk — `run thread` (Slack half)

`pg-desk run thread <id> --change added|changed|removed|sweep` (Phase 13, docket `pg2-2j5ac.40`,
Slack half) is the design-level rule (see [`pipeline-run.md`](pipeline-run.md)): a Slack thread
change cross-references that thread to the PR(s)/issue(s) its permalink or text references, then
re-runs stage 2 (interpret) for every PR currently linked, without a fresh gather and without
re-running sync. This is the Slack-side counterpart of [`run-issue.md`](run-issue.md)'s Jira half,
independently checkpointable from it — a rework of the Slack transport does not hold the Jira half.

## What triggers it

A Slack thread the configured identity is mentioned in, or otherwise surfaced by the Slack MCP,
changing, being added, or being swept — typically via the ZR desk-thread role's own `thread-me`
feed wiring (this docket's ZR-wiring sibling packet), which is what actually triggers this in the
live system.

`--change`'s four values (`added`/`changed`/`removed`/`sweep`) are accepted uniformly: none of
them changes this command's own dispatch. The flag exists only so the caller can still log or
route on it, and there is no `--change removed` special case here — this command has no stored
"is this thread already known" state of its own to consult (unlike gather's `pr show` re-read), so
a thread `pg-connector` cannot fetch is treated as a hard failure regardless of `--change`.

## Cross-referencing (thread -> PR)

For the triggering thread, `run thread`:

1. Fetches the thread's current state via `pg-connector thread show <id>` (the `pg-connector` CLI
   dispatch — never a Go import of the thread capability's provider).
2. Scans its text for two kinds of reference, using the SAME two mechanisms this docket's Jira-half
   sibling packet's own gather step already established — never a third, parallel extractor:
   - **A GitHub PR permalink** anywhere in the text (a new, purpose-built scanner — this text is
     free-form prose, unlike a `<pr>` command-line argument's own end-anchored shape). Resolves
     DIRECTLY to a PR entity id, always against the one configured repository (Phase 9's
     single-repository scope) regardless of which owner/repo the URL itself names.
   - **A Jira ticket key** anywhere in the text, recognized by the SAME configured
     `ticket_patterns` list [`gather.md`](gather.md)'s ticket-key scan uses (never a hardcoded
     pattern, never a second parser). Resolves INDIRECTLY: a ticket key is not itself a PR, so it
     is resolved via whichever PR(s) that Jira-half ticket-key scan has ALREADY cross-referenced to
     the same key (the `xref` table's reverse, to-id-keyed lookup). A key with no linked PR yet
     produces no xref write for that key — a no-op, not a speculative guess.
3. Writes/confirms one `xref` row per match — `(repo, pr, <pr-id>, thread, <thread-id>)`, evidence
   `permalink` or `ticket-key` — the SAME "pr" anchors the "from" side convention the Jira half
   already established for its own `(pr, issue)` rows, just pointed at `thread` instead of `issue`.
4. Looks up every PR CURRENTLY cross-referenced to this thread (the `xref` table's reverse lookup,
   `to_type="thread"`) — never just the matches step 2 found this run: a previously-matched PR
   whose reference the thread's current text no longer repeats stays linked, mirroring the Jira
   half's own identical store-driven re-interpret rule.
5. Re-runs interpret (stage 2 only — never gather, never sync) for EACH linked PR, against
   whatever facts the store already has for it.
6. Writes zero beads, on every linked PR, exactly like the Jira half.

A thread whose text contains neither a permalink nor a recognized ticket key writes no xref row
and triggers no re-interpret — unless an EARLIER run already linked a PR to it, in which case step
4/5 still re-interpret that pre-existing link (step 2's own scan found nothing new, but nothing was
un-linked either). If more than one PR is linked, every one is attempted; a failure re-interpreting
one does not skip the rest.

## Exit codes

Same contract as every other `run` invocation (see [`pipeline-run.md`](pipeline-run.md)): exit `0`
on success, on a degraded re-interpretation, or on the "no reference at all" no-op case; exit `1`
when the triggering thread cannot be fetched at all, an xref write or lookup fails, a linked PR's
re-interpretation fails, or the store cannot be read/written; MUST NEVER exit `9` or return a raw
`pg-connector` exit code.

## Telemetry and logs (D24)

`run thread` emits nothing over OpenTelemetry or Prometheus through Phase 13 (export is a later
observability item, resolved by the observability review `pg2-7kizi`, alongside pg-router's own
metrics sink). Its own thread-fetch/cross-reference step logs nothing of its own; the outcome folds
into `run`'s existing structured JSON log line to stderr, emitted once PER linked PR
re-interpreted (each logged against that PR's own entity id, exactly like the Jira half's own N-PRs
-> N-lines convention) — a thread with zero currently-linked PRs (no match this run, and none
carried over from an earlier one) produces no log line at all, since there is nothing to
re-interpret and therefore nothing to log against.

## Out of scope

The gather-stage addition that reads a PR's own already-linked threads back out (Phase 13's eighth
input) is documented in [`gather.md`](gather.md), not here — that is a read gather performs, not
something this command writes. The Slack incident/urgency signal is explicitly not carried
(deferred with `pg2-jpfw.5`): this command never computes or forwards one. ZR's desk-thread role
wiring (the `thread-me` feed caller that invokes this command in the live system) is this docket's
own ZR-wiring sibling packet. A Jira-shaped or thread-shaped human view (a dedicated UI/CLI surface
built around Slack threads as first-class objects) is a later design, not this command.
