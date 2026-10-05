# pg-desk — hide, unhide, wip

`pg-desk hide <pr> [reason]`, `pg-desk unhide <pr>`, and `pg-desk wip on|off <pr>` write the
`annotation` table only — never any pipeline table (see [`store-schema.md`](store-schema.md)).
`<pr>` MUST accept `OWNER/REPO#N`, a PR URL, or a bare number when the store holds exactly one
repository or the current working directory resolves one.

A hidden PR MUST be excluded from the five panel arrays `serve` and `open` produce, and exposed
only in the `hidden` array — interpret does not consider hidden or WIP state at all (see
[`interpret.md`](interpret.md)); that state is joined only at read time. `wip on` MUST NOT
convert a ready PR to a draft upstream: that upstream conversion is an accepted, recorded loss
for this whole window (D15); `wip` here only ever records the annotation.

## Typed forms

The three verbs also exist under each entity type: `pg-desk <type> hide <id> [reason]`,
`pg-desk <type> unhide <id>` and `pg-desk <type> wip on|off <id>`, for `<type>` one of `pr`,
`issue`, `thread` (entity-change-flow design 6.9). The top-level verbs above are unchanged and keep
working until the cutover is complete everywhere. The typed forms depend on the store's schema:

- On an old-schema store, `pg-desk pr hide|unhide|wip` behave exactly like the top-level verbs. The
  `issue` and `thread` forms have no old-schema counterpart and refuse with the store's error,
  which says to run `pg-desk migrate --cutover`.
- On a migrated store they write the key/value annotations (see [`annotate.md`](annotate.md)):
  `hide` sets `hidden` to `{"value": true, "reason": <reason or null>}`, `unhide` sets it to
  `{"value": false, "reason": null}`, and `wip on|off` sets `wip` to `true`/`false`. `hidden` and
  `wip` are independent keys. Each write appends one `annotation_changed` record. An id with no
  stored entity exits `1`.

## Exit codes, telemetry, and logs

`0` on success (the annotation is written, or already in the requested state); `1` when `<pr>`
does not resolve to exactly one stored entity (ambiguous or unknown), or the store cannot be
written. No other exit code is used by these commands in Phase 9.

These commands emit nothing over OpenTelemetry or Prometheus (D24). They carry no
structured-JSON logging contract — only ordinary CLI error text on failure — since they are
direct annotation writes, not pipeline runs.

## Out of scope (Phase 9)

Resolving `<pr>` against more than one configured repository is out of scope this phase. The
upstream draft-conversion side effect of `wip on` is an accepted loss for the whole window D15
describes, not just this phase.
