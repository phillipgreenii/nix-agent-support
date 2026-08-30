# Premise Freshness — probe reference

Full probe table for **F-3** and the runnable script for **F-9**'s `decided-against?` probe.
Moved out of the main skill body to keep it within budget (tc-ql0o Stage C, C.1/C.2); the main
`SKILL.md`'s Premise Freshness section points here.

## F-3 probe table

For each external referent the premise NAMES — a commit, an external ticket, a
file/module/path, a code symbol, a sibling bead, a derived identifier — the agent MUST run
the matching NAMED PROBE below and MUST record its decisive output verbatim. `main` means
the repo's primary branch; run each from the repo in question.

| Probe                  | Command                                                                                                                                                                                                                         | ⇒ STALE (premise moot / recorded value wrong)                                                                                                                                                                | ⇒ STILL LIVE (as recorded)                                                         |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| **`landed?`**          | `git merge-base --is-ancestor <sha> main; echo $?`                                                                                                                                                                              | `0` — the commit IS on main, so "maybe still unlanded" is already answered                                                                                                                                   | `1` — genuinely not on main                                                        |
| **`pushed?`**          | `git fetch --quiet origin && git branch -r --contains <sha>`                                                                                                                                                                    | any output (e.g. `origin/main`) — already pushed, so "close once the push happens" is satisfied                                                                                                              | EMPTY output. Read the OUTPUT, not `$?` — the exit status is `0` either way        |
| **`patch-identical?`** | `git cherry -v main <branch>`                                                                                                                                                                                                   | no output, or every line starts with `-` — an equivalent patch is already upstream under a DIFFERENT sha, so the branch is spent                                                                             | any line starting with `+` — that commit is genuinely not upstream                 |
| **`path-exists?`**     | `git ls-tree -r --name-only main -- <path> [<path>…]`                                                                                                                                                                           | a path the plan edits is ABSENT from the output — but ABSENT is AMBIGUOUS (**F-9**): only once `decided-against?` finds NO ruling does it mean that module is GONE and a design prescribing edits to it void | every named path echoes back. Read the OUTPUT, not `$?`                            |
| **`decided-against?`** | `bd list --desc-contains '<artifact>' --status all -n 0 --json`, then `rg -in 'operator ruled\|not to be committed\|superseded'` over those beads AND over the artifact's own untracked on-disk copy (full runnable form below) | any recorded operator ruling forbidding the work — the ABSENCE IS the executed decision, so re-proposing the work is the defect                                                                              | no ruling in the artifact and none in ANY citing bead — absence means not-done-yet |
| **`symbol-shape?`**    | `git grep -c -- '<symbol>' main -- <path>`                                                                                                                                                                                      | exit `1`, no output — the option/function/field the steps operate on no longer exists at that path                                                                                                           | exit `0` with `main:<path>:<n>` — still present                                    |
| **`ticket-open?`**     | `pjira issue <KEY> \| jq -r '.status'`                                                                                                                                                                                          | `Closed` / `Done` / `Resolved` — the external work finished, so "continue `<KEY>`" is moot                                                                                                                   | anything else. `pjira`'s JSON is FLAT: `.status`, never `.fields.status`           |
| **`sibling-open?`**    | `bd show <sib-id> --json \| jq -r '.data[0].status'`                                                                                                                                                                            | `closed` — the bead this one waits on, duplicates, or defers to is done                                                                                                                                      | `open` / `in_progress` / `blocked`                                                 |
| **`next-free-id?`**    | `printf '%04d\n' "$(( 10#$(git ls-tree -r --name-only main -- docs/adr \| rg -o '/(\d{4})-' -r '$1' \| sort -n \| tail -1) + 1 ))"`                                                                                             | DIFFERS from the number the draft recorded — that id is TAKEN by someone else; renumber before landing                                                                                                       | equals the recorded number                                                         |

- A probe reading is decisive ONLY when it resolves. `fatal: Not a valid commit name`
  (exit `128`) means this clone does not know that sha; a missing repo, an unreachable ticket,
  or a referent the premise never names precisely enough to probe are all the same case: the
  agent MUST read it as STILL LIVE and MUST NOT call the premise moot. Ambiguity is never
  evidence of mootness.
- **Exception, not ambiguity**: a non-resolving `tc-*` id cited in a bead's body or comment
  (`sibling-open?` / `bd show <id>` returning "no issue found") is NOT the ambiguous case above
  — it is decisive. It means the referenced bead was deleted by a completed-bead prune (the
  2026-08-08 prune, or a future one), not that the id is mistyped or the reference
  still-unverified. Operator ruling (`tc-2n1h`, 2026-08-23): if the referenced content mattered,
  it was quoted inline into the citing bead at prune time, so nothing is lost by treating the id
  as gone. Read a non-resolving `tc-*` id as PRUNED and stop probing it; do not read it as STILL
  LIVE and do not re-file or re-derive the sibling. This exception is scoped to `tc-*` bead-id
  references specifically — it does not extend to other referent kinds (a commit sha, a ticket
  key, a file path), which remain governed by the general ambiguity rule above.
- If the premise names NO external referent, the agent MUST record that fact explicitly
  rather than skip the step silently — an unrecorded check is indistinguishable from a
  skipped one.
- REVIEW QUALITY IS NOT A STALENESS SIGNAL. That a draft/plan was adversarially reviewed, had
  findings fixed, or had its details verified against live source says only that it was
  accurate WHEN WRITTEN; a thorough review of a snapshot ages exactly as fast as the snapshot.
  An agent MUST NOT accept "it was already reviewed", "it looks plan-ready", or an approving
  review verdict in place of running these probes.
- A premise proven moot MUST terminate in a CLOSE, not another park/re-park/release — but
  CLOSE-AS-SUPERSEDED MUST EXTRACT, NOT DISCARD. Before closing, the agent MUST read the stale
  work and, if it makes a claim that CURRENT source VIOLATES (a defect it predicted, a decision
  it called load-bearing that the shipped version skipped), MUST file that as its own issue
  linked back to the original (`bd create … --deps "discovered-from:<id>"`) and MUST name the
  new id in the close reason. A blind close is forbidden.
- A DERIVED IDENTIFIER recorded at draft time (next-free ADR number, sequence id, "highest
  accepted is N") MUST be recomputed at land time and MUST NOT be trusted as recorded; between
  drafting and landing, someone else takes it.

## F-9 `decided-against?` runnable form

ABSENCE IS AMBIGUOUS. An ABSENT `path-exists?` reading means EITHER "not done yet" OR "RULED
AGAINST — the absence IS the executed decision", and the two demand OPPOSITE actions. Before
treating any absence as work to do — and specifically BEFORE briefing a subagent to create,
restore, or commit the missing artifact — the agent MUST run this probe and MUST record its
output. Absent from `git` is NOT absent from DISK: an artifact deliberately left uncommitted
still exists untracked, and its own header is the usual place the ruling is written (see the
Superseding Rulings rule in core, which requires the ruling be recorded verbatim there).

```bash
A='<artifact-name>'; R='operator ruled|not to be committed|do not commit|decided against|superseded'
fd -HI "$A" "${PN_WORKSPACE_ROOT:-.}"                # EXISTS untracked? that alone is a signal
rg -uu -in "$R" -g "*$A*" "${PN_WORKSPACE_ROOT:-.}"  # the artifact's OWN header usually carries it
bd list --desc-contains "$A" --status all -n 0 --json |
  jq -r '.data[] | "== \(.id) ==\n\(.description)\n\(.notes)"' | rg -in "$R"
```

`bd search` matches TITLE and ID only, so it MUST NOT be used here; `bd list --desc-contains`
is the description search, and `--status all -n 0` is load-bearing — the bead holding the
ruling is usually CLOSED and the default both excludes closed and caps at 50 rows (verified
`pg2-xx1y5`: without `--status all`, 3 of the 6 citing beads are missed, the incident's own
among them). A hit is DECISIVE: the work MUST NOT be re-proposed, and the close-as-superseded
rule above applies. An UNRUN probe MUST NOT be read as "no ruling exists".
