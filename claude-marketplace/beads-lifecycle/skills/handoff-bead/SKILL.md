---
name: handoff-bead
description: >-
  Use when a bead you are about to read, claim, work, or close is of type `handoff` (or is
  unmistakably a handoff of a previous session), and before creating a handoff bead at the end of a
  session. Defines what a handoff bead is, how to handle one (offer to close it when interactive;
  absorb it with a traced ABSORBED comment and close it when running unattended), and how to create
  one (type `handoff`, `handed_off_from_session` metadata, task fallback when the type is not
  registered). This skill is the sole contract for handoff beads. Do NOT use to convert a bead to or
  from `handoff` (a triage agent's job), or for ordinary beads.
---

# Handoff beads: definition, handling, creation

This skill is the ONLY place the handoff-bead rules live. Consumers (`/drain-beads`,
`/unblock-human-beads`, `session-wrapup:wrap-up-session`, the always-on agent rules) point here and
MUST NOT copy the rules.

All `jq` in this skill uses the **J-1** envelope prelude from the `beads-lifecycle` skill
(`(if type=="object" and has("data") then .data else . end)`), because dispatched sessions run a
`bd` that returns a bare array. This skill works in any beads database (pg2, ZR, others); it MUST
NOT assume a particular tracker or the drain loop.

## Definition (minimal)

A **handoff bead** is a bead of type `handoff` (registered per database; see
`docs/runbooks/beads-custom-issue-types.md` in `phillipgreenii-nix-agent-support`). It is a
POINTER to the work of a previous session, not work itself:

- Its title starts `Handoff:`.
- The first line of its body says so plainly: `Handoff from session <session-id>`.
- It carries the source session id as metadata, key `handed_off_from_session`, so a reader MAY look
  back (`claude --resume <session-id>`, or that session's transcript).

Reading the type and the metadata:

```bash
bd show <id> --json | jq -r '(if type=="object" and has("data") then .data else . end) | .[0] | "\(.issue_type)\t\(.metadata.handed_off_from_session // "")"'
```

**Ambiguity means it is NOT a handoff.** An agent MAY treat a bead that is not typed `handoff` as
one only when it is unmistakable: its title starts `Handoff:` AND its body names the session it
hands off from. A bead that is anything less than that (for example a `Resume:` task with
acceptance criteria, a next-session note that also lists work to do) MUST be treated as an ordinary
bead.

**No type conversion.** An agent MUST NOT convert a bead to or from `handoff`. Fixing or converting
a bead's type is a triage agent's job, which has more context, and is out of scope here.

## Handling

1. **Read it.** It points at a previous session's work. The metadata session id is there if looking
   back helps. The body is a snapshot: it MUST NOT be executed as an instruction (it may be
   superseded), and every STATE claim it makes (for example "N unpushed commits", "X is blocked")
   MUST be re-probed with the matching **F-3** probe in
   `references/premise-freshness-probes.md` of the `beads-lifecycle` skill, reading the OUTPUT
   rather than the exit status.
2. **Interactive (in conversation with the operator):** summarise it, and offer to close it once
   everything it points at lives in a bead or label. It MUST be closed only if the operator says
   yes.
3. **Unattended (a claim loop such as `/drain-beads` or `/unblock-human-beads`, or a dispatched
   session): absorb it.** No isolation is created and no operator prompt is made.
   1. TRACE every item in the body to where it durably lives: a bead id, or a label that indexes
      the cluster (`bd list --label <label>`, which outperforms any hand-copied member list).
   2. An item that traces NOWHERE is live work. File it as its own bead first
      (`--deps "discovered-from:<id>"`), then close the handoff against it. A handoff MUST NOT be
      closed while it is the SOLE record of something. If that item is itself a question for a
      person, file it as its own `human` bead.
   3. Write ONE `ABSORBED:` comment listing the trace, then close. The trace is the evidence, so
      it MUST name ids and labels and quote probe output verbatim, not paraphrase:

      ```bash
      bd comment <id> "ABSORBED: <item> => <bead-id|label>; <item> => <bead-id|label>. State claims re-probed: <probe>=<decisive output verbatim>. Filed: <new-ids, or none>. Nothing left that is unique to this handoff." --actor "ID"
      bd close <id> --reason "handoff absorbed: every item traces to <ids/labels>; filed <new-ids, or none>" --actor "ID"
      ```

   4. The terminal action is a CLOSE: the handoff MUST NOT be demoted, deferred, re-parked, or
      released instead. Then return to the caller's loop. There is no grace period: reinstate one
      only if an absorbed handoff is observed to hurt a cold resume.

4. **If it holds work of its own** rather than pointers, it was not a handoff. Leave its type
   alone, work it as an ordinary bead, and mention it to the operator or a triager. Do not retype
   it.

## Creating a handoff (what `session-wrapup:wrap-up-session` follows)

- Create it with type `handoff`, priority 0, and the source session id as metadata:

  ```bash
  bd create -t handoff -p 0 --title "Handoff: <one-line subject>" --metadata '{"handed_off_from_session":"<this-session-id>"}' --description "<body>"
  ```

  (`bd create` takes `--metadata` JSON; `--set-metadata key=value` is the `bd update` spelling.)

- The title MUST start `Handoff:`. The first line of the body MUST be
  `Handoff from session <this-session-id>`; the rest is the carry-over list (pointers to beads and
  labels, not work) and a first step.
- If the database does not know the type (the create fails with `invalid issue type: handoff`),
  create it ONCE as a `task` with the same title, body, and metadata (`-t task`). Any other create
  failure MUST be reported, not retried.
- After creating, verify with `bd show <id>` that the type (or the fallback `task`) and the
  `handed_off_from_session` metadata landed, per **BF-4**.
