---
disable-model-invocation: true
description: >-
  Stop the /drain-beads loop running in this session: stop claiming new
  beads, finish whatever is already claimed, hand off to
  session-wrapup:wrap-up-session, then report a summary of what changed.
---

# /stop-draining-beads

Stop claiming new beads in this session. Finish the bead this session
already has claimed, following `/drain-beads`' own steps through to a
normal close (or a normal park, if it genuinely can't finish) — don't force
a park just because you were asked to stop. If this session hasn't claimed
anything, there's nothing to finish here.

Then invoke the `session-wrapup:wrap-up-session` skill to close out the
session.

Finally, report a summary of what this session did — wrap-up-session
already reports beads closed/filed and repos landed; add any bead this
session parked. Do not report on unpushed local commits.
