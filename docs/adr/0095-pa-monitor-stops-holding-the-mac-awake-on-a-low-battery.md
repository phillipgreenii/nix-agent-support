# pa-monitor stops holding the Mac awake on a low battery

**Status**: Accepted (resolves `pg2-s86z9`; extends ADR 0024's keep-awake rule)
**Date**: 2026-10-14
**Deciders**: Phillip Green II

## Context

pa-monitor holds a `caffeinate` subprocess whenever any session is working or a nudgeable error has
had no attempt yet (ADR 0024's keep-awake rule: `keepAwake := anyWorking || anyUnattemptedNudgeableDisrupt`).
It never read the power source or the charge. On 2026-10-09 and 2026-10-10 the Mac lost AC power,
kept running agent load under keep-awake assertions, and drained its battery to 1% to 3%; the local
telemetry stack stalled for about 42 to 46 minutes of awake time each time, and macOS then hibernated
the Mac at 1% (bead `pg2-bqcqi`, which holds the evidence). `pa_monitor_caffeinate_active` read 1 at
every 15-minute point while the battery fell, and the charge went from 30% to the 3% low-battery
warning in about 27 to 34 minutes.

pa-monitor is a candidate holder, not a proven sole one: other processes also held idle-sleep
assertions. Releasing this daemon's hold is still the cheapest source-side fix, because the load that
burned the battery was the agent pool this daemon supervises.

## Decision

1. **A power-source reader is a Strategy.** `internal/core/power` defines `Reader`
   (`Read() (Reading, error)`) with three implementations: `DarwinReader` (parses `pmset -g batt`,
   read-only, cached for 15 seconds), `NoopReader` (reports AC; used off macOS) and `FakeReader` (the
   test double). `NewReader` picks by platform. Only a confirmed `Battery Power` source counts as
   on-battery; a UPS or a Mac with no battery reads as AC.
2. **The caffeinate manager applies a battery rule on top of the keep-awake rule.** While on battery
   at or below the HOLD threshold (default 30%) no subprocess is started, whatever the toggle and the
   keep-awake signal say. While on battery at or below the RELEASE threshold (default 20%) a running
   subprocess is stopped at the next tick. The normal rule resumes when AC returns or the charge
   exceeds HOLD plus a hysteresis margin (default 5 points), which keeps a charge hovering at the
   threshold from flapping the assertion. Between RELEASE and HOLD a running subprocess is kept.
3. **A failure to read the battery fails open.** An error, or a battery reading with no charge,
   leaves the normal rule in force and clears the hysteresis latch, so an unreadable battery never
   releases a hold.
4. **Nothing is killed or nudged.** Sessions simply stop being protected from idle sleep; the Mac can
   then sleep with its state intact. Forcing the Mac to sleep and a low-battery alert are out of
   scope and tracked separately.
5. **The rule is observable.** `pa_monitor.caffeinate.battery_releases_total{reason}` counts each
   release (`release_threshold`, `spawn_blocked`); the `pa_monitor.caffeinate.active` gauge reads 0
   while the rule withholds the assertion, even with the toggle on, because the gauge answers
   "is the Mac being held awake"; and `status --json` carries `caffeinate.battery`
   (`gate`, `on_battery`, `percent`).
6. **Configuration follows the existing daemon option path.** The thresholds are
   `caffeinate_battery_hold_pct`, `caffeinate_battery_release_pct` and
   `caffeinate_battery_hysteresis_pct` in `config.toml`, surfaced as the Home-Manager options
   `caffeinate.batteryHoldPercent` and `caffeinate.batteryReleasePercent`. A hold of 0 disables the
   rule; a release above the hold is capped at it (and refused by a module assertion). Like every
   other daemon setting they are read at startup (ADR 0028), so a change applies on restart.

## Consequences

- On battery below 30% a working session no longer prevents idle sleep through this daemon, so a
  long agent run on a low battery can be interrupted by sleep. That is the intended trade: the
  alternative is the 1% hibernation and a stalled host.
- The `pa_monitor.caffeinate.active` gauge no longer equals the toggle while the rule is withholding
  the assertion; the toggle remains visible as `caffeinate.mode` in `status --json`. A consumer that
  read the gauge as "toggle on" must read `mode` instead.
- Between RELEASE and HOLD the gauge still reads 1 when a subprocess was already running, because it
  is still holding the Mac awake until the charge reaches RELEASE.
- A `pmset` spawn happens at most every 15 seconds; a threshold crossing is therefore noticed within
  one cache window plus one tick.
- Any other holder of an idle-sleep assertion (audio output, a browser, another `caffeinate`) still
  keeps the Mac awake; this decision does not touch them.
