# Plan: claude-tool-tracker (design section 11, chunks 1 to 7)

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail in code, tests,
comments or docs). Design of record: `docs/superpowers/specs/2026-10-14-claude-tool-tracker-design.md`
(approved by the operator 2026-10-29). This plan only orders and splits the design's seven chunks into
landable tasks; every decided-how stays in the design and is cited, never restated here. Tracking bead
`pg2-y73ue4`.

## Ground rules carried from the design

- Each task lands on its own (design goal G6) and is tested from real fixtures that stay in the repo
  (design C8, global rule V-3). Live checks are only the ones in design section 10.
- Shadow mode (design 8.1): CETA keeps recording exactly as today until a per-machine cut-over.
- **Cut-over gate (design 8.3, operator ruling 2026-10-29).** CETA MUST NOT drop its recording
  (`PermissionRequest`, `PermissionDenied`, `PostToolUseFailure`, `SessionEnd` hooks; writes to
  `tool_decisions`, outcomes, `decision_trace_entries`) on a machine until the tracker is observed
  recording on that machine. Therefore:
  1. Task T14 builds the per-machine switch with the CETA side OFF by default (a machine nobody has
     checked keeps CETA's recording). It removes nothing by itself.
  2. Task T15 is the only task that flips the switch, for this laptop only. It is blocked by the
     task that observes the tracker recording on this laptop (T6) and by the comparison task (T10),
     and its acceptance requires recorded evidence of that observation and the operator's acceptance
     of the comparison.
  3. No other task may delete or disable any CETA recording path.

## Task map

| Task | Design chunk | Title                                                                                   | Size | Blocked by                                   |
| ---- | ------------ | --------------------------------------------------------------------------------------- | ---- | -------------------------------------------- |
| T1   | 1            | Package skeleton, envelope spec and the `append` command                                | S-M  | none                                         |
| T2   | 1            | `ingest` into SQLite, published views, stable `call_id`, skipped-line counts            | M    | T1                                           |
| T3   | 1            | Settlement derivation (`settled_by`, `settlement`, `converted_ask`, `unresolved`)       | S-M  | T2                                           |
| T4   | 2            | Plugin command hooks and the home-manager module (default-enable parity)                | S-M  | T1                                           |
| T5   | 2            | The tracker mod (core kinds)                                                            | M    | T1                                           |
| T6   | 2            | Live checks 1 to 4, shadow mode starts, tracker observed recording on this laptop       | S    | T4, T5, T2                                   |
| T7   | 3            | CETA writes `ceta.*` events (still also writing its own database)                       | S-M  | T1                                           |
| T8   | 4            | CETA read-only adapter over the published views, `--source`, labels join                | S-M  | T3, T7                                       |
| T9   | 4            | `compare-tracker` command                                                               | S-M  | T8                                           |
| T10  | 4            | Run the comparison after the shadow days and record the result in the design            | S    | T6, T9                                       |
| T11  | 5            | Miss analysis on the tracker source (`evaluate`, `report`, `show`, hook-misses skill)   | M    | T8, T10                                      |
| T12  | 6            | `claude-tool-tracker timing`                                                            | M    | T3, T6 (T7 for classes: optional enrichment) |
| T13  | 6            | `identify-slow-tools` skill                                                             | S    | T12                                          |
| T14  | 7            | Per-machine cut-over switch (CETA side OFF by default, nothing removed)                 | S-M  | T10, T11                                     |
| T15  | 7            | Cut-over on this laptop (flip the switch here, after observed recording and acceptance) | S    | T14, T6, T10                                 |

Chunk 1 is split in three (envelope/append, ingest, settlement) because ingest and settlement derivation
are separately reviewable halves; chunk 2 is split four ways (hooks plus module, mod, live checks) for the
same reason; chunk 4 is split into adapter, command and the time-gated run; chunk 6 and chunk 7 keep the
design's own "then" ordering.

## Task notes

### T1. Package skeleton, envelope spec, `append`

Create `packages/claude-tool-tracker` (Go module, nix package, go test check, README with the published
line format) following the layout of existing Go packages such as `packages/pg-keep-awake`. `append` writes
exactly one line per event, with one append-mode write, to
`$XDG_STATE_HOME/claude-tool-tracker/events/tool-events.<session_id>.<source>.jsonl` (design 5.1, 5.2), and
doubles as the hook entry point for T4 (design 5.3, last paragraph). Builder choices in design section 9
(maximum line size and shortening) are made here and documented in the README.

### T2. `ingest`, schema, views

Reads only files matching the full pattern, tolerates unknown kinds, counts skipped lines per file,
remembers read offsets, assigns the stable integer `call_id` at first ingest, applies retention
(design 4, 5.2, 7.1 "Call ids", 9). The SQLite schema is the builder's; the published views are the
contract to readers. T2 MUST write the view names, columns and meaning to
`packages/claude-tool-tracker/docs/views.md`, and every later reader task cites that file.

### T3. Settlement derivation

Implements the section 6 table and the pending/unresolved rule over the T2 schema, recomputed when later
events arrive, exposed through the published views.

### T4. Plugin hooks and HM module

New plugin `claude-marketplace/claude-tool-tracker` with command hooks `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `PermissionRequest`, `PermissionDenied` invoking the bare `append`-family command
(design 5.3, C5); module under `home/programs/claude-tool-tracker` whose `enable` defaults to
`claude-code.enable` (design C7). Consumes the T1 hook-mode contract.

### T5. The mod

TypeScript mod writing `tracker.call_start`, `.check`, `.running`, `.call_end`, `.session_end` by handing
events to `append` through `$.process.run` with the session id passed explicitly (design C1 to C4, 5.3, 9).
Re-check steps of the spike document run first (design C4).

### T6. Live checks and shadow start

After T4 and T5 are applied on this laptop, run design section 10 checks 1 to 4, write results back into the
design, and record the observation that the tracker is recording on this laptop (events files and an ingest
run showing calls). Shadow mode starts; CETA recording is untouched (design 8.1, 8.3).

### T7. CETA `ceta.*` events

Go, in-process write to CETA's own `tool-events.<session>.ceta.jsonl` from `PreToolUse` (design 5.4).
CETA's database writes stay. A failure to write an event MUST NOT alter the decision (design 2 anti-goals).
Measure before and after if tracing is turned on for every call (design 9).

### T8. Adapter

Read-only open of the tracker database, mapping the published views to the row type used by `evaluate`,
`report` and `show`; `--source ceta|tracker`, default stays `ceta`; labels keyed by `session_id` +
`tool_use_id` joined in; no mixing of histories (design 7.1).

### T9. `compare-tracker`

The five comparison outputs of design 8.2, plus a report of a session with CETA activity and no tracker file
(design 12, first risk).

### T10. Comparison run

After a couple of shadow days run `compare-tracker` on this laptop, record the result in the design, and
confirm live check 5 from the skipped counts (design 8.2, 10).

### T11. Miss analysis

The settlement grading dimension and ranking (design 7.1 table), correctness check graded against
`settlement`, `show` of `ceta.trace`, `identify-hook-misses` updated with its command interface unchanged.
The default `--source` stays `ceta` here; flipping it belongs to T15.

### T12 and T13. Timing and `identify-slow-tools`

`claude-tool-tracker timing` per design 7.2 (T12); the skill that files deep-dive beads from the top of both
rankings (T13).

### T14. Switch

The per-machine mechanism (design 8.3: a CETA home-manager module option set by the consuming flake, or
another way). Default: CETA keeps all recording. With the switch off, behavior is byte-identical to today.
With it on, CETA performs the section 8.4 actions. T14 flips nothing anywhere.

### T15. Cut-over here

Flip the switch for this laptop only, only after T6's observation and T10's accepted comparison; `--source`
default becomes `tracker` on that machine (design 8.4). Another machine joins by enabling the tracker,
repeating live checks 1 to 4 and passing the comparison there (design 8.3); that is out of this plan.

## Out of scope

Background-shell ownership, an ingest daemon, approval-wait timing, "No" versus Esc, the other
`pg2-tbbpt` workstreams and any change to CETA's decision engine (design section 2, non-goals).
