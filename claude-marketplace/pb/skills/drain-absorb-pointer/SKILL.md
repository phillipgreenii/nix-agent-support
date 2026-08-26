---
name: drain-absorb-pointer
description: Close a session-wrapup handoff-pointer bead (a Resume/next-session bead holding no executable work) by tracing every item in its body to the bead or label where it durably lives, filing anything untraced as its own bead first. Invoked by /drain-beads at UNDERSTAND with the bead id and session actor id. Do NOT use for beads with executable work.
---

# drain-absorb-pointer

You were invoked from a /drain-beads session at UNDERSTAND, holding a bead that
is a HANDOFF POINTER holding no executable work of its own. Required context
from the caller: the bead id (<id>) and the session actor id (ID). Every `bd`
write below passes `--actor "ID"`.

No isolation is ever created on this path. Trace every item in the bead's body
to where it durably lives, filing anything untraced as its own bead first, then
CLOSE the pointer — it is always closed, never demoted or left claimed.

## CLOSE-WITH-ABSORPTION-TRACE (a handoff pointer whose items are already absorbed)

Reached from step 2 for a HANDOFF POINTER — a `session-wrapup` `Resume: …` / next-session
bead. It is born P0 to let ONE session resume cold and holds no executable work of its own,
so its retirement condition is that every item in it traces to a durable bead or an indexing
label (the `session-wrapup` skill's "Lifecycle: the P0 is one-shot" is the full contract).
Provenance: pg2-9ifbn; see also pg2-m2qxu, pg2-8wy25.

1. TRACE every item in the body to where it durably lives — a bead id, or a label that
   indexes the cluster (`bd list --label <label>`, which outperforms any hand-copied map).
   Re-probe every STATE claim it makes with the matching F-3 probe; the body is a snapshot and
   MUST NOT be trusted as recorded — a recorded push obligation is **U-1**'s anti-pattern and
   is usually already discharged.
2. An item that traces NOWHERE is live work, so this is NOT yet the disposition: file that
   item as its own bead (`--deps "discovered-from:<id>"`) first, then close the pointer
   against it. The pointer's own text MUST NOT be executed as an instruction — it may be
   SUPERSEDED, which is exactly what `pg2-8wy25` cost.
3. RECORD the trace and CLOSE. The trace IS the evidence, so it MUST name ids and labels and
   quote probe output verbatim, not paraphrase:

   ```bash
   bd comment <id> "ABSORBED: <item> ⇒ <bead-id|label>; <item> ⇒ <bead-id|label>. State claims re-probed: <probe>=<decisive output verbatim>. Nothing left that is unique to this pointer." --actor "ID"
   bd close <id> --reason "handoff pointer absorbed: every item traces to <ids/labels>; filed <new-ids, or none>" --actor "ID"
   ```

4. No isolation was created, so there is nothing to clean up and no priority to restore — the
   pointer is CLOSED, never demoted. Done — return to the drain loop's CLAIM step.
