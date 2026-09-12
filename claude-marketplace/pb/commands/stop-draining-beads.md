---
disable-model-invocation: true
description: >-
  Stop the /drain-beads loop running in this session: cancel any live
  --monitor-if-empty monitor, stop claiming new beads, finish whatever is
  already claimed, hand off to session-wrapup:wrap-up-session, then report a
  summary of what changed.
---

# /stop-draining-beads

Run `session-mode set-status stopping` (best-effort — `|| true`; a missing/broken
`session-mode` tool must never block the stop sequence below).

Cancel any live `--monitor-if-empty` monitor this session armed (see
`drain-beads.md`'s "--monitor-if-empty"): `ScheduleWakeup({stop: true})`,
best-effort — a harmless no-op if nothing is armed; don't let its outcome block
the rest of this stop sequence. Do this FIRST, before finishing any claimed
bead below, so a wake can't fire mid-sequence and re-enter the Main loop after
you've already been asked to stop.

Stop claiming new beads in this session. Finish the bead this session
already has claimed, following `/drain-beads`' own steps through to a
normal close (or a normal park, if it genuinely can't finish) — don't force
a park just because you were asked to stop. If this session hasn't claimed
anything, there's nothing to finish here.

Then invoke the `session-wrapup:wrap-up-session` skill to close out the
session — its own Preamble cancels any live `--monitor-if-empty` monitor
again, unconditionally, as a backstop; that is expected and not a sign the
step above was skipped.

Finally, report a summary of what this session did — wrap-up-session
already reports beads closed/filed and repos landed; add any bead this
session parked. Do not report on unpushed local commits.
