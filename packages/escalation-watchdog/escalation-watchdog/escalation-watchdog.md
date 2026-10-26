# escalation-watchdog

> Hand beads that stayed `escalated` too long to the operator, marked `stale-escalation`.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Hand off every bead escalated for more than 12 hours (at most 3 per run):

`escalation-watchdog --bd-dir {{/path/to/tracker/root}} --stale-after {{12h}}`

- See what a run would hand off without writing anything:

`escalation-watchdog --bd-dir {{/path/to/tracker/root}} --stale-after {{12h}} --dry-run`

- Raise the per-run cap and skip beads touched in the last 10 minutes:

`escalation-watchdog --bd-dir {{/path/to/tracker/root}} --stale-after {{90m}} --max-per-tick {{10}} --quiet-for {{10m}}`

- Test with a fixed clock (epoch seconds or an RFC 3339 UTC timestamp):

`ESCALATION_WATCHDOG_NOW={{2026-10-26T12:00:00Z}} escalation-watchdog --bd-dir {{/path/to/tracker/root}} --stale-after {{12h}} --dry-run`
