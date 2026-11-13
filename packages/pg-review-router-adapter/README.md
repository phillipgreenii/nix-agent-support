# pg-review-router-adapter

The small program between a pg-router command role and `pg-review`. It owns every
router concern, so `pg-review` itself stays free of router concepts (no items, events
or schedules). It makes no scheduling decision: no time-of-day, no last-run memory.

```text
pg-review-router-adapter run <item-id> <schedule_id> <lens> <scope> <window>
pg-review-router-adapter maintain
```

`pg-review` is found on `PATH` and run as a child (the adapter must map its exit code,
so it cannot `exec` it). The adapter logs to stderr.

## run

- `<item-id>` is the router item's fire time, an RFC 3339 timestamp (an offset such as
  `-05:00` is accepted). It is the window end and is passed to `--to` verbatim.
- `<window>` is `<positive integer>h` or `<positive integer>d` (anything else exits 2).
  `from` is `to` minus the window.
- The first log line carries the computed `from` and `to`.
- It runs `pg-review job run <lens> --scope <scope> --from <from> --to <to>
--run-key <key> --schedule-id <schedule_id>`.
- The run key is `<lens>/<scope>/<window>/<window end>`: the window string as received
  and the window end as UTC RFC 3339 at second precision with a `Z` suffix, for example
  `agent-workflow/workspace/24h/2026-11-05T07:00:00Z`. A daily and a weekly schedule of
  one lens and scope ending at the same instant therefore never share a key.

### What `7d` means

A `d` is 24 **elapsed hours** (`7d` is exactly 168 hours), not a calendar day. The
arithmetic runs on instants, in the offset the item timestamp carries, and never reads
the time zone database or the `TZ` variable, so a window that spans a daylight saving
change is still exactly 168 hours long (its local wall-clock start differs by an hour
from the end's wall clock on the other side of the change). The program calls no
external `date`, so macOS (BSD) and Linux (GNU) cannot differ; the tests run under
several `TZ` values to prove it.

## Exit codes

| `pg-review` exit | adapter exit | note                                      |
| ---------------- | ------------ | ----------------------------------------- |
| 0                | 0            |                                           |
| 20               | 0            | skipped or already done; one log line     |
| 9                | 0            | run lock busy (shared Busy); one log line |
| others           | unchanged    | the router treats them as handler errors  |
| killed by signal | 1            |                                           |

A usage error (wrong arguments, bad timestamp or window) exits 2; a missing
`pg-review` exits 1.

## SIGTERM

The router sends SIGTERM to the whole process group and SIGKILL 5 seconds later. The
adapter traps SIGTERM and keeps waiting for `pg-review`, which received the same signal
itself and closes its session within the grace. The adapter forwards no signal.

## maintain

Runs `pg-review job maintain` with the same exit-code mapping.

## Tests

`nice -n 10 go test -p 4 -timeout 120s ./...` from this directory. Hermetic: a fake
`pg-review` shell script on a temp `PATH` records its argv and signals.
