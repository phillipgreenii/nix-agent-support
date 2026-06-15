# pr-pool: eliminate the feedback-discovery join (bead-structure spec B)

Bead: `pg2-ktqh`. Blocks `pg2-kplb` (spec C: externalize roles/prompts/queries
to TOML). Sequenced **after** spec A (stop-on-`done` + run-query/run-role harness,
merged `fbe23eb`) and **before** spec C.

> Code references below are relative to `packages/<name>/`, e.g.
> `pr-pool/internal/discover/discover.go:85` and
> `pg-pr/internal/sync/sync.go:1483`. Note there are **two** `sync.go` files in
> pg-pr — `cmd/pg-pr/sync.go` (small) and `internal/sync/sync.go` (the engine);
> every `sync.go` ref here means the latter.

## Context

pr-pool turns the bead store's ready queue into role→bead dispatches. Feedback
discovery (`pr-pool/internal/discover/discover.go:85`, `discoverFeedback`)
currently does two lookups per candidate:

1. **Cycle identity** — `iss.Type == "task" && strings.HasPrefix(iss.Title,
"process-feedback:")`. A title string-match.
2. **Ownership (the JOIN)** — `bd show <parent> --json`, then keep the candidate
   iff `parent.Metadata["author"] == selfLogin` (`discover.go:101-106`). Ownership
   lives on the **parent** merge-request bead, not on the cycle, so discovery has
   to fetch the parent for every candidate.

Lookup (2) is the join this spec removes. It exists because pg-pr's
`processFeedback` runs for **both** my PRs and team PRs (`pg-pr/internal/sync/sync.go:362`:
"Local-bead phases … run for both subsets"; `processFeedback` is called
unconditionally at `sync.go:455`, while the upstream-write `maybePromoteDraft` is
gated on `mineSet` at `sync.go:465`). So open processing-cycle beads exist for
others' PRs too; the join is what narrows discovery down to mine.

The fix moves ownership onto the cycle bead **at creation time**. pg-pr's
PR-watcher already knows the PR author when it creates the cycle
(`CreateProcessingCycle` is called at `sync.go:1483`, where both `pr.Author` and
`e.isSelfAuthored(pr.Author)` — the method at `sync.go:861` — are in scope), so it
can stamp ownership directly. Discovery then becomes a pure `bd ready` arg
variation with no parent fetch — which is also what lets spec C express queries as
plain arg lists with no join logic.

### Decisions taken during brainstorming (2026-06-15)

- **Scope: feedback-only, minimal.** No custom issue_type. The `process-feedback:`
  title prefix stays as the cycle-identity marker. The worker path is untouched.
- **Ownership encoding: a single `mine` label.** Self-owned cycles are stamped
  `mine`; team cycles are left unlabeled. Discovery is `bd ready --label mine`.
  (Accepted trade-off: an unstamped cycle is indistinguishable from a team cycle
  and is silently skipped — so the migration must be airtight.)
- **No mine/theirs on work/worker beads.** Ownership is a cycle-bead concept only.
  There is no reviewer-over-others' role today, so worker beads (all created off
  my own cycles) need no ownership signal.
- **Backfill is an agent-run migration step, not committed code.**
- **The `self_login` precheck is preserved on the daemon path.** `resolveSelf`
  validated that pg-pr's config is readable and `self_login` is set; that guard is
  kept where it operationally matters (the `drain` daemon path). The now-unused
  parameter is dropped from the discovery call chain, and the manual `run-query`
  smoke command stops resolving `self_login` (it no longer needs it).

## Decision

### 1. Stamp `mine` at cycle creation (pg-pr)

`CreateProcessingCycle` (`pg-pr/pkg/beads/processingcycle.go:35`) gains an
ownership parameter:

```go
func (c *Client) CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error)
```

When `mine` is true, `-l mine` (`bd create`'s `-l, --labels` flag) rides on the
single `bd create` call, so the cycle is born stamped; the parent-child `dep add`
remains a separate call afterward, exactly as today (`processingcycle.go:43` then
`:59`). At create time the cycle has no parent, so there is nothing to inherit and
`--no-inherit-labels` is moot. When `mine` is false, no label is added — team
cycles stay unlabeled.

The sole call site (`sync.go:1483`, inside `processFeedback`, `sync.go:1313`)
becomes:

```go
id, err := bdc.CreateProcessingCycle(ctx, prBeadID,
    fmt.Sprintf("%s#%d", repo, pr.Number), e.isSelfAuthored(pr.Author))
```

The `BeadClient` interface declaration (`sync.go:110`) and the package-level
convenience wrapper (`processingcycle.go:232`, `func CreateProcessingCycle(...)`)
update to match. No behavior change for team PRs: they were never discovered
before and still are not.

### 2. Drop the join and the unused `selfLogin` param (pr-pool)

`discoverFeedback` (`discover.go:85`) drops the per-candidate `bd show <parent>`
entirely:

```go
func discoverFeedback(ctx context.Context, br beads.Runner, role roles.Role) ([]DispatchContext, error) {
    issues, err := beads.Ready(ctx, br, "--label", "mine")   // self-relative by construction
    if err != nil {
        return nil, fmt.Errorf("discover feedback: bd ready: %w", err)
    }
    var out []DispatchContext
    for _, iss := range issues {
        if iss.Type == "task" && strings.HasPrefix(iss.Title, "process-feedback:") {
            out = append(out, DispatchContext{Role: role, BeadID: iss.ID})
        }
    }
    return out, nil
}
```

`bd ready --label mine` is AND-semantics single-label filtering (confirmed:
`bd ready`'s `-l, --label` = "must have ALL"); only cycle beads carry `mine`
today, but the `type==task && title~"process-feedback:"` guard is kept as
defense-in-depth and as the explicit cycle-identity marker (the chosen scope keeps
the title prefix).

`selfLogin` is removed from the discovery call chain, because nothing in discovery
reads it once the join is gone:

- `discoverFeedback` — param removed (above).
- `ForRole` (`discover.go:71`) — param removed; the `if selfLogin == ""` guard
  (`discover.go:74`) is removed with the join it protected.
- `Discover` (`discover.go:44`) — param removed.
- `Orchestrator.DrainOnce` (`pr-pool/internal/orchestrator/orchestrator.go:67`) —
  param removed.
- `runRunQuery` (`pr-pool/cmd/pr-pool/runrole.go:69`) — its conditional
  `resolveSelf`/`selfLogin` block (`runrole.go:83-93`) is removed, and the
  `ForRole` call (`runrole.go:94`) loses its last argument. (`runRunRole`,
  `runrole.go:34`, already does not resolve `self_login` — `runrole.go:31-33` — so
  it is unchanged.)
- `runDrain` (`pr-pool/cmd/pr-pool/drain.go:51`) — the `DrainOnce` call
  (`drain.go:69`) drops its `selfLogin` argument. `resolveSelf` is still called for
  its precheck side-effect (see below), so its return value is no longer used at
  the call: `if _, err := resolveSelf(ctx); err != nil { … }`.

**Doc comments to rewrite** (they describe the join being removed): the package
doc (`discover.go:1-3`, "Feedback cycles are owned by self (the parent
merge-request bead's author)"), `Discover` (`discover.go:40-43`), `ForRole`
(`discover.go:67-70`), and `runRunQuery` (`runrole.go:66-68`, "It resolves
self_login because the feedback query's parent-author join needs it").

**The precheck is preserved on the daemon path.** `resolveSelf` (`drain.go:77`,
which shells `pg-pr config show --json` and errors when `self_login` is empty via
`parseSelfLogin`, `drain.go:86,93`) still runs in `runDrain` (`drain.go:51`) — the
operational daemon path — as a startup validation: pr-pool now trusts that pg-pr
stamped `mine` correctly, which only holds if pg-pr's `self_login` is configured,
so failing fast there is still wanted. `parseSelfLogin` stays (used by
`resolveSelf`). The manual `run-query feedback` smoke command no longer prechecks
`self_login`, because it no longer needs the value — an acceptable narrowing of
the precheck to the path that actually runs unattended.

### 3. Migration — agent-run one-time backfill (no code)

After the code lands, the deploying agent runs a one-time stamp of any open `mine`
cycles whose parent PR is self-authored. There are **0 open process-feedback
cycles today** and cycles drain fast (created on new feedback, closed when
processed), so this is **belt-and-suspenders insurance**, not a backlog drain — it
only matters for a self-owned cycle that is open _at the instant of cutover_.

```bash
# SELF_LOGIN from `pg-pr config show --json | jq -r .self_login`
bd list --type=task --status=open --json --limit 0 \
  | jq -r '.[] | select(.title|startswith("process-feedback:"))
                 | select((.labels//[])|index("mine")|not) | .id' \
  | while read -r cycle; do
      parent=$(bd dep list "$cycle" --direction=down --json | jq -r '.[0].id // empty')
      [ -z "$parent" ] && continue   # cycle whose best-effort dep add failed: skip, no parent to resolve
      author=$(bd show "$parent" --json | jq -r '.metadata.author // ""')
      [ "$author" = "$SELF_LOGIN" ] && bd update "$cycle" --add-label mine
    done
```

`bd dep list <cycle> --direction=down` returns what the cycle depends on — its
parent PR bead — because `CreateProcessingCycle` wires `dep add <cycle> <pr>
--type=parent-child` (`processingcycle.go:59`), i.e. the cycle is the dependent.
(This is the inverse of `ListChildrenOfPR`, which uses `--direction=up` to list a
PR's children, `processingcycle.go:171`.)

**Cutover sequence (orphans nothing):**

1. Ship the pg-pr stamping change (§1). New cycles are now stamped; the old
   pr-pool join is still running and still discovers everything correctly.
2. Run the backfill snippet above (likely a no-op).
3. Ship the pr-pool discovery flip (§2).

The old join keeps working for any unlabeled cycle until step 3, so no self-owned
cycle is ever silently skipped. A cycle created in the overlap window (after step
1, before step 3) is both correctly stamped _and_ discovered by the still-running
old join; discovery only reads and dispatch claims the bead atomically, so the two
paths converging on the same self-cycle cannot double-dispatch.

This assumes cycles are **not reopened** across the cutover — true today (cycles
close on processing and are not reopened). If cycle lifetimes ever lengthen, a
pre-cutover unstamped cycle that reopened would be silently skipped.

## Testing

### Deterministic (CI safety net)

- **pg-pr `CreateProcessingCycle` stamps `mine`.** The existing cycle tests use a
  real bd workspace (`newBDWorkspace(t)`, `processingcycle_test.go:52,82,99,139`),
  not an arg-capturing fake `Runner`. So assert the label by reading it back
  (`bd show`/issue labels) on the real workspace for `mine=true` and asserting its
  absence for `mine=false` — or add a fake-`Runner` arg-capture test (the package
  already has a `NewClientWithRunner` seam at `processingcycle_test.go:42`). Either
  way, `TestCreateProcessingCycle_CreatesAndLinks` and the sibling real-workspace
  tests get the new signature; existing parent-child-link assertions are unchanged.
- **pg-pr call site:** a test confirms a self-authored PR's cycle is stamped `mine`
  and a team PR's cycle is not. Assert against the cycle that `processFeedback` /
  `Sync` actually creates — not the pre-seeded `selfCycle`/`teamCycle` pair at
  `sync_test.go:1127,1131` (those are explicit `CreateProcessingCycle` seeds in
  `TestSync_SkipsAndWarnsOnTeammateReplyDraft`, so they wouldn't observe the new
  stamping). Either extend that Sync test to assert on the engine-created cycle or
  add a focused `processFeedback` test.
- **pg-pr signature ripple (compile-only):** changing the `CreateProcessingCycle`
  _method_ signature touches **every** call site — do not hand-maintain a list;
  `grep -rn CreateProcessingCycle packages/pg-pr` is the source of truth. Pass
  `false` where ownership is irrelevant. Note especially **two** `BeadClient`
  interface stubs — `noopBeads` (`internal/sync/sync_test.go:596`) **and**
  `stubBeads` (`cmd/pg-pr/sync_test.go:61`); missing the latter leaves the
  `cmd/pg-pr` package not compiling. Also update the package-level wrapper at
  `processingcycle.go:232` **and its body** (`:233`).
- **pr-pool `discoverFeedback`:** with the test fake `Runner`
  (`routingRunner`, `discover_test.go:14`), assert the feedback query is
  `bd ready --label mine` and that **no `bd show` is issued** (the join is gone);
  assert the `type`/title guard still filters non-cycle beads. The fake's
  `show`/`showErr` fields become dead. Note the fake's _routing_ must change too:
  `routingRunner.Run` currently sends **any** `ready` call containing `--label` to
  the worker branch (`discover_test.go:29`), so it must be taught to distinguish
  `--label mine` (feedback) from `--label worker-ready` (worker), or the rewritten
  feedback test gets the wrong canned response.
- **pr-pool discover tests — remove** (they exist only to test the join/ownership
  being deleted): `TestDiscover_skipsBeadOnParentShowError` (`discover_test.go:180`),
  `TestDiscover_emptySelfLoginErrors` (`:140`), `TestForRole_feedbackEmptySelfLoginErrors`
  (`:234`), `TestDiscover_feedbackDisabled_emptySelfLoginOK` (`:242`).
- **pr-pool discover tests — rewrite** (behavior + arity): `TestDiscover_feedbackOwnership`
  (`:55`, becomes "queries `--label mine`, no `bd show`"), `TestForRole_feedbackOwnershipBypassesEnabled`
  (`:204`), `TestForRole_workerIgnoresSelfLogin` (`:222`, name now stale),
  `TestForRole_unknownKindErrors` (`:256`).
- **pr-pool discover tests — arity only** (drop the `selfLogin` positional arg):
  `TestDiscover_workerLabelFilter` (`:79`), `TestDiscover_skipsDisabledRole`
  (`:101`), `TestDiscover_orderFeedbackThenWorker` (`:127`),
  `TestDiscover_propagatesReadyError` (`:151`), `TestDiscover_propagatesWorkerReadyError`
  (`:169`).
- **precheck relocates to a new cmd test:** the empty-`self_login` "fail fast"
  assertion moves out of discover into a new `drain` cmd-layer test (no such cmd
  test exists today). `run-query` no longer prechecks `self_login`, so it has no
  such test.

### Live (manual, full-stack)

- `pr-pool run-query feedback` (spec A's harness) shows the matching cycles via
  `bd ready --label mine` with **no parent-author fetch during discovery** — the
  visible confirmation that the ownership join is gone. (`run-query` still issues a
  per-dispatch `bd show` purely to print id/type/title, `runrole.go:100`; that
  display lookup is unrelated to the removed join and survives.)
- One real cycle dispatched end-to-end via `pr-pool run-role feedback <id>`.

## Consequences

### Positive

- Feedback discovery is one `bd ready` call instead of `1 + N` (`bd ready` plus a
  `bd show` per candidate). No per-candidate parent fetch against the dolt server.
- Discovery is a pure arg list — the precondition spec C needs (no join logic, no
  filter DSL in config).
- Ownership is explicit on the bead it describes, instead of inferred via a parent
  walk.

### Negative / risks

- **Silent-skip on unstamped cycles.** An open self-cycle without `mine` reads as
  "team" and is never discovered. Mitigated by the cutover sequence (old join runs
  until the flip) and the backfill step. A stray unlabeled self-cycle after cutover
  would go unprocessed with no error — acceptable given cycles are short-lived, are
  not reopened, and the count is currently zero.
- **Two writers must agree on "self."** pg-pr stamps `mine` relative to its own
  `self_login`; pr-pool trusts the label. Both run as the same operator, and the
  retained `resolveSelf` precheck (daemon path) guards against a misconfigured
  pg-pr/pr-pool side.

### Neutral

- **`metadata.author` on the merge-request bead is retained, unchanged.** It is
  still written at `pg-pr/pkg/beads/mergerequest.go:351` and remains the source of
  truth for pg-pr's disappeared-PR ownership partition (`internal/sync/detector.go:133`)
  and for the worker/feedback LLM nudges (`pr-pool/internal/roles/roles.go:75`
  asserts "metadata.author is me"; `:72` resolves the parent PR bead). The new
  `mine` cycle label is **derived from** `metadata.author` at creation time, not a
  replacement for it; only feedback _discovery_'s use of the author is removed.
- The `process-feedback:` title prefix remains the cycle-identity contract, shared
  by pr-pool discovery and pg-pr's `FindOpenProcessingCycle`. Replacing it with a
  custom issue_type is explicitly out of scope (see below).

## Out of scope / deferred

- **Custom issue_type for cycles.** Would let both pr-pool and pg-pr's
  `FindOpenProcessingCycle` drop the title-prefix match, but it reaches into the
  duplicate-cycle detection just fixed for the "48 cycles for 27 PRs" bug. Separate
  bead if the title match ever bites.
- **`mine`/`theirs` on worker beads + a reviewer-over-others' role.** Worker beads
  are created by the feedback role's LLM and are all `mine` by construction today;
  a symmetric ownership label is premature until a reviewer role exists.
- **Worker query change.** Stays `bd ready --label worker-ready --exclude-label
human`.

## Alternatives considered

- **Single `mine` label vs. an `owner:self`/`owner:other` pair.** The pair would
  positively mark team cycles (making a future reviewer query a clean `--label
owner:other` and making unstamped cycles auditable). Rejected in favor of the
  single label for minimalism, matching the existing single-purpose label style
  (`worker-ready`, `human`); the reviewer role that would benefit from the pair
  does not exist yet.
- **A committed `pg-pr backfill-cycle-ownership` subcommand.** Rejected as
  throwaway code for an empty, self-draining problem; an agent runs the snippet
  once at cutover instead.
- **Drain-and-sequence with no explicit backfill at all.** Relies purely on cycles
  draining before the flip. Rejected as slightly less airtight than running the
  explicit (if usually no-op) backfill snippet.
- **Removing `resolveSelf` entirely.** Rejected — it doubles as a config precheck
  that is still wanted on the daemon path; only the unused parameter is dropped
  (and the manual `run-query` path's now-pointless resolution).
