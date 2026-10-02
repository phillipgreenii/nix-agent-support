---
name: handoff-bead
description: >-
  Use when a bead you are about to read, claim, work, or close is of type `handoff` (or is
  unmistakably a handoff of a previous session), and before creating a handoff bead at the end of a
  session. Defines what a handoff bead is, how to handle one (offer to close it when interactive;
  absorb it with a traced ABSORBED comment and close it when running unattended, except a handoff
  labelled `human`, which is left for the operator), and how to create one (the `handoff-create`
  script: type `handoff`, `handed_off_from_session` metadata, `human` iff a human is in the
  session, task fallback when the type is not registered). This skill is the sole contract for
  handoff beads. Do NOT use to convert a bead to or from `handoff` (a triage agent's job), or for
  ordinary beads.
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
- It carries the `human` label **iff it was created ATTENDED** (a human was in the session and asked
  for or approved the handoff); an **unattended** handoff carries no `human` label. See "Attended
  and unattended handoffs".

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

## Attended and unattended handoffs

Operator ruling (Phillip, 2026-10-02): "if a handoff bead was created while working with a human,
then the created bead should also be labeled human. the idea is that if i'm working with an agent
and signal to do a handoff, then i would want to continue working with them. i don't want a drain
agent picking it up before i start the next session. (if a handoff bead was created by an agent
when it is running out of context (or some other reason), then that could be picked up by a drain
agent."

- **ATTENDED** = a human is in the session and asked for or approved the handoff. The bead MUST
  carry `human`, so no claim loop takes it before the operator starts the next session.
- **UNATTENDED** = no human is present: the agent ran out of context, an auto-trigger run, any
  agent-initiated handoff. The bead MUST NOT carry `human`, so a drain agent MAY pick it up and
  absorb it (Handling, step 3). Provenance labels such as `auto-session-wrapped` are separate and
  are kept (see the `beads-lifecycle` skill's `auto-session-wrapped` section, **AW-1**..**AW-3**).
- The caller MUST decide which; `handoff-create` has no default. When unsure whether a human is
  present, ask the human if there is one to ask; with no human to ask, it is unattended.

## Handling

1. **Read it.** It points at a previous session's work. The metadata session id is there if looking
   back helps. The body is a snapshot: it MUST NOT be executed as an instruction (it may be
   superseded), and every STATE claim it makes (for example "N unpushed commits", "X is blocked")
   MUST be re-probed with the matching **F-3** probe in
   `references/premise-freshness-probes.md` of the `beads-lifecycle` skill, reading the OUTPUT
   rather than the exit status.
2. **Interactive (in conversation with the operator):** summarise it, and offer to close it once
   everything it points at lives in a bead or label. It MUST be closed only if the operator says
   yes. A handoff labelled `human` is exactly the one the operator left to continue with an agent:
   offer to resume from it, and offer the close only once it has been consumed.
3. **Unattended (a claim loop such as `/drain-beads`, or a dispatched session): absorb it.** No
   isolation is created and no operator prompt is made.

   **EXCEPTION: a handoff labelled `human` is NOT absorbed unattended.** It was created ATTENDED
   (see "Attended and unattended handoffs"); it exists so the operator can continue with an agent,
   so an unattended session MUST leave it untouched: no ABSORBED comment, no close, no isolation,
   no retype, no label change, no demotion or deferral. If the session holds a claim on it, release
   it per **B-1**/**B-2** (`--status open --assignee ""` in one call) and change nothing else. It
   is left for the interactive offer (step 2). `/unblock-human-beads` does not claim handoffs at
   all (its claim query excludes type `handoff`), so a `human` handoff is not worked by either
   queue.

   For a handoff with NO `human` label:
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

Create it with the **`handoff-create`** script, which the home-manager module
`phillipgreenii.programs.handoff-create` installs on `PATH` wherever Claude Code is enabled. An
agent MUST NOT assemble the `bd create` by hand: the type, priority, title prefix, first body line,
metadata, label policy, fallback, and read-back are mechanical details a model gets wrong or
forgets, and the script makes them deterministic and tested. If `handoff-create` is not on `PATH`,
report that and ask the operator to run `pn workspace apply`; do not improvise a hand-built create.

1. **Decide attended or unattended** (see "Attended and unattended handoffs"). There is no default.
2. **Write the body to a fresh file** (a unique name; do not reuse an earlier body path). It holds
   the carry-over list (pointers to beads and labels, not work) and a first step. It MUST NOT
   contain the `Handoff from session ...` line; the script writes it.
3. **Run the script:**

   ```bash
   handoff-create --attended   --session-id "<this-session-id>" --title "<one-line subject>" --body-file "<body-file>"
   handoff-create --unattended --session-id "<this-session-id>" --title "<one-line subject>" --body-file "<body-file>" [--label auto-session-wrapped]
   ```

   Options: `--label L` (repeatable, passthrough), `--actor ID` (default: the session id),
   `--bd-dir DIR` (the tracker root, **BF-1**; default: the caller's cwd). `<this-session-id>` is
   this run's real, observed session id, never fabricated.

What the script does, so a caller need not (and MUST NOT) redo it:

- Creates a bead of type `handoff`, priority 0, titled `Handoff: <subject>` (a `Handoff:` already
  at the start of the subject is not doubled), whose body starts `Handoff from session <id>`
  followed by the body file, with metadata `{"handed_off_from_session":"<id>"}`. (`bd create`
  takes `--metadata` JSON; `--set-metadata key=value` is the `bd update` spelling.)
- Adds the `human` label iff `--attended`; `--label human` with `--unattended` is refused.
- If the database does not know the type (the create fails with `invalid issue type: handoff`),
  retries ONCE as a `task` with the same title, body, metadata, and labels. Any other create
  failure is reported (exit 3) and NOT retried. Each create runs once and is never chained with
  `||` to a parse step (**B-7**).
- Reads the bead back with `bd show --json` (either envelope shape, per **J-1**) and verifies the
  type (or the `task` fallback), title prefix, first body line, priority, metadata key, and `human`
  present iff attended (**BF-4**). It prints the new id on stdout and what it verified on stderr;
  a disagreement exits 4 and names the id.

Exit codes: 0 created and verified; 1 unexpected error (for example `bd` not installed); 2 usage
error, nothing created; 3 `bd create` failed, nothing created; 4 created but the read-back
disagrees. A wrap-up that gets 2 or 3 MUST report it, not fall back to a hand-built `bd create`.
