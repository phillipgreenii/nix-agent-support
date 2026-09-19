# pg-wi-flow

Bead-workflow CLI framework: query/list/reserve/claim/release work items, render their working
context, annotate/record-verdict/advance/create-child/merge/close them, and escalate/resolve
attention-axis blockers -- driven by a two-layer JSON config and the built-in null workflow
(one stage, work: entry AND closing, concerns: `[]`) until a real workflow is configured.

This package (bead tc-q25wo, docket tc-9ddu3.1) ships two components: the `pg-wi-flow` CLI itself
and `pg-wi-flow-identity`, a `claude-extended-tool-approver` input processor. See
`packages/pg-wi-flow/pg-wi-flow-identity/pg-wi-flow-identity.md` for the identity processor's own
tldr page; this README covers the CLI.

## Verb surface

Every verb routes its `bd` calls through `lib/tracker.bash` (the Adapter -- the only file in this
package that invokes `bd`), and composes its actor via `lib/actor.bash` (`$PG_WI_FLOW_IDENT-<stage>`
inside Claude Code; an explicit `--actor` override only outside it).

### Read / reservation core (tc-9ddu3.1.1)

- **`query [--stage S]... [--attended]`** -- print the fully built `bd` filter set for the
  requested query shape, one flag/value per line.
- **`list [--stage S]... [--attended] [--questions] [--unpooled] [--stale --days N | --stale --reserved-hours H]`**
  -- print (as a JSON array) the items the query would admit, or one of the special
  `--unpooled`/`--stale` views.
- **`next [--stage S]...`** -- reserve the next claimable item (leaf, or a container's descent
  per the state model), printing `ID STAGE WORKFLOW` or `none`.
- **`claim ID`** -- transfer a reservation to the caller's identity, printing
  `ID STAGE WORKFLOW` followed by the full assembled prompt.
- **`release ID`** -- release `ID`, clearing the assignee in the same call as the status change.

### Render engine (tc-9ddu3.1.2)

- **`context [--render] ID [--role reviewer --concern C | --role researcher --gap G]`** --
  classify `ID` and resolve its kind/instructions/checklist/concerns/duplicates/applicable
  docs/lessons/premise/siblings/workflow. Without `--render`, prints `WI_*` lines; with
  `--render`, prints the fully assembled prompt.
- **`explain ID`** -- classification, stage, workflow, kind, component, premise state, open
  questions, round count, and the current holder.
- **`history ID`** -- the verdict and round trail (`metadata.wi_verdicts`).
- **`duplicates ID`** -- title/key-term duplicate candidates plus same-component related
  candidates.
- **`docs ID`** -- the applicable-docs candidate search on its own.

### Item-content and stage-transition write verbs (tc-9ddu3.1.3)

- **`annotate ID [--kind K] [--component C] [--premise P] [--acceptance T] [--append-description T] [--append-notes T] [--design T]`**
  -- the only way to write item content; flags are combinable in one call.
- **`record-verdict ID --concern C --json <file|->`** -- run by a reviewer leaf; records `C`'s
  verdict for the current round in item metadata. Stdin allowed via `-`.
- **`round ID`** -- reads every verdict recorded for the current round, merges them, increments
  the round counter, and prints the merged verdict (`ready`, `gaps`, `blocked`, `duplicate ID`, or
  `related IDs`) plus `WI_MUST_ESCALATE=true` when the counter reaches `iteration_bound` without
  `ready`.
- **`advance ID --to STAGE [--reason TEXT]`** -- validates the target stage exists in `ID`'s
  workflow (a move to a lower-order stage requires `--reason`), swaps the stage label, and adds
  the container label if `ID` has any children. Never creates a land bead.
- **`create-child PARENT --title T [--kind K] [--stage S] [--blocked-by ID]... [--description T]`**
  -- new child at the workflow's entry stage (or `--stage`, validated against the child's
  inherited workflow). `PARENT` gains the container label in the same call and stays open. Each
  `--blocked-by` adds a blocking edge from the child to `ID` (repeatable).
- **`merge ID... --into SURVIVOR`** -- closes each listed `ID` as a duplicate of `SURVIVOR` with
  related links; `SURVIVOR` gets a note listing the merged symptoms.
- **`close ID --reason TEXT [--trace "BULLET=DISPOSITION"]...`** -- closes `ID`. With `--trace`,
  refuses unless every bullet parsed from `ID`'s description has a disposition (an existing id, a
  plain label, or `filed:TITLE` to file a new entry-stage item).
- **`close-duplicate ID --of OF`** -- refuses unless `ID` is newer than `OF` and `OF` is open on a
  fresh read; adds a related link.

### Attention-axis write verbs (tc-9ddu3.1.4)

- **`escalate ID [--question T --trigger t]...`** -- with `--question`/`--trigger` pairs: `ID` is
  the blocked work item. Computes a fingerprint per pair; reuses an OPEN question already carrying
  it (blocking edge added, nothing created) or creates a new question child (labeled
  `question`+`escalated`+the trigger). With no `--question` at all: `ID` must already be a
  question -- bumps it from `escalated` to `human` (the resolver could not settle it).
- **`resolve ID (--decision D --rationale R | --answer A | --abandon --reason-code r | --defer d)`**
  -- exactly one outcome. `--decision`/`--answer` record and close (the parent resumes);
  `--decision` refuses a `q:intent` question unless the actor's role is `main`. `--abandon
--reason-code <moot-premise|superseded|wont-do|duplicate>` records and closes; `moot-premise` also
  files a groom-stage follow-up; if this was the parent's last open blocker, the parent is closed
  too. `--defer` defers. A legacy `human` item (not a question) is accepted too: `--answer` appends
  to notes, removes `human`, releases; `--abandon` closes; `--defer` defers.

### Global options (before `COMMAND`)

- **`--actor NAME`** -- explicit actor override. Accepted ONLY outside Claude Code (refused when
  `PG_WI_FLOW_IDENT` is already set -- inside Claude Code the actor always composes from
  `PG_WI_FLOW_IDENT` plus the item's stage).
- **`-h, --help`** -- show the help message.
- **`-v, --version`** -- show version information.

`pg-wi-flow --help` is the authoritative, always-current copy of this surface; this README exists
for browsing without a checkout.

## Configuration

Two-layer JSON config, machine layer deep-merged under a repo layer (repo wins):
`$XDG_CONFIG_HOME/pg-wi-flow/config.json`, then `<repo>/.claude/wi-flow/config.json`. See
`packages/pg-wi-flow/data/README.md` for the `paths.defaults` plugin-data-root contract, and
`packages/pg-wi-flow/lib/config.bash` for the loader itself.
