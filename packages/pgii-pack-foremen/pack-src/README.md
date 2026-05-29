# pgii-foremen — explicit category-triage pack

Five agents implementing the design in
`docs/superpowers/specs/2026-05-28-triage-extension-design.md`:

- **triager** — wakes on `type=triage` beads in hq (or on mail);
  classifies into city/zr/personal; emits work beads in the
  destination rig's db; closes the triage bead with a structured
  reason.
- **city-foreman** — watches `hq` for newly-arrived work beads;
  enhances (priority, labels, AC-fill); handles HQ-worker escalations.
- **zr-foreman** — same shape, watches `zr` db.
- **personal-foreman** — same shape, watches the personal db(s) per
  the topology choice pinned in the spec's verified-outcomes
  section; additionally handles rig-picking for ambiguous personal
  work and creates tracking beads for multi-rig work.
- **city-worker** — HQ-scope worker. Mirrors pgii-workers/worker
  but city-scoped, since HQ is not a rig.

## Topology

```
                      ┌─────────────────────────────┐
       you ─chat──▶  │ mayor / operator (HQ)       │
                      │  • inline triage when present│
                      │  • emit bead via             │
                      │    gc --rig=X bd create      │
                      │  • else: open type=triage    │
                      │    bead in hq                │
                      └────┬────────────────────────┘
                           │ async / unclear
                           ▼
                      ┌─────────────────────────────┐
                      │ triager (HQ on_demand)      │
                      └────┬────────────────────────┘
                           │
        ┌──────────────────┼─────────────────┐
        ▼                  ▼                  ▼
  ┌──────────┐       ┌──────────┐       ┌────────────────────┐
  │ city-    │       │ zr-      │       │ personal-foreman   │
  │ foreman  │       │ foreman  │       │ (+ rig-picking +   │
  │ (HQ)     │       │ (zr db)  │       │   tracking beads)  │
  └────┬─────┘       └────┬─────┘       └─────┬──────────────┘
       ▼                  ▼                    ▼
  ┌──────────┐       ┌──────────┐       ┌────────────────────┐
  │ HQ       │       │ zr       │       │ per-rig workers    │
  │ worker   │       │ workers  │       │ (0-1 per rig × 6)  │
  │ (0-1)    │       │ (0-3)    │       │                    │
  └──────────┘       └──────────┘       └────────────────────┘
```

## Foreman idempotency

Each foreman tags beads it has enhanced with the label
`foreman-triaged:<utc-ts>` and excludes labeled beads from its
work_query. The label is foreman-internal — workers do NOT check
it. The actual "untriaged" gate is the dolt db itself: work that
hasn't been routed lives in `hq` as `type=triage`; once routed,
it's in the target rig's db and workers can claim it.

## Worker escalation

When a worker discovers a bead doesn't belong in its rig:

```bash
gc bd update <bead-id> --add-label="gc:escalation" --assignee="" --status=open
gc mail send <category>-foreman \
  -s "ESCALATION: wrong-rig <bead-id> [HIGH]" \
  -m "Bead is for <suspected rig>, not this rig. Evidence: ..."
```

The `gc:escalation` label is excluded from both worker and foreman
work_queries, so the bead is parked until the foreman wakes via mail,
removes the label, and either re-emits (correct rig, same category),
re-triages (wrong category), or clears the escalation as-is.

## Migration note

Hand-built pack. Future migration to a nix derivation is tracked
as a follow-up to the original triage-extension epic
(`gc-2oqf9`). When that migration lands, the source path in
`pack.toml` flips from `assets/imports/pgii-foremen` to
`/nix/store/<hash>-pgii-foremen-0.1.0`.
