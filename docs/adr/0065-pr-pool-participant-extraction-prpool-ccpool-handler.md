# pr-pool: participant extraction into `packages/prpool-ccpool-handler` (Phase 5)

**Status**: Proposed — pending operator review (docket `pg2-84o3m` Task 5.0 requires review of
this ADR before Phase 5 execution begins; see "Operator review" below)
**Date**: 2026-09-10
**Deciders**: drafted by an implementer agent under Task 5.0's derive-plan mandate; the
command-executor open question (the Decision's "Open question resolved" section) is this ADR's own to make per that mandate's
"Freedom boundary" — everything else pins the docket's already-fixed contract verbatim

## Context

`ADR 0061`'s Decision, "Participant model: full message-passing is the target; the in-process
shortcut is register-held intent," records the operator's already-settled direction: pr-pool's documented
participant architecture — every core→participant interaction as a message carrying a per-call
tracking id, an inline-or-deferred reply, and a callback for a deferred ack (`INV-INTF-1`,
`INTF-HANDLER`, `INTF-SOURCE`) — **is the intended target, not a description to relax to match the
code**. The shipped in-process shortcut (`Offer` standing in for `handler.dispatch`, `query.Run`
standing in for a source query message, concrete tool drivers compiled into the generic binary) is
**consciously kept for now** and recorded in the realization-gap register as settled-but-unreached
intent. `ADR 0061` is explicit that extracting the participant interfaces for real — minting
tracking ids, giving deferred replies a callback, registering participants, moving the
`ccpool`/`beads`/command-tool drivers out of the generic binary — is **Phase 5's job, not any task
before it**, and that nothing before Phase 5 is authorized to invent a partial or ad hoc messaging
shim.

This docket's Phases 0-4 have since landed real, incremental progress against that target without
crossing the extraction line itself: Task 2.2 mints a per-offer `dsp-<12 hex>` dispatch tracking id
in-process (`internal/eventqueue/queue.go`); Task 2.3 makes the busy/decline machinery reachable
end-to-end for a `command` role (`executor.ErrBusy` → `eventqueue.DeclineBusy`,
`internal/orchestrator/listener.go`); the push half of `INTF-SOURCE` now crosses as a real message
(`internal/core/ingest.go`, `internal/emit/socket.go`); `INV-LIFE-2`'s gate mechanism and Phase 3's
socket verbs are in; the `sessions`/`reconcile` subcommands' realization-gap rows are already
annotated "register-held: deployment-coupled, moves out at extraction"
(`packages/pr-pool/docs/decisions/cli.md`'s `DEC-CLI-2`). None of this crossed a wire — every call
listed above is still a same-process Go call. The realization-gap register
(`packages/pr-pool/docs/behavior/README.md`) currently holds eleven rows whose "Where the
implementation stands" column names exactly this gap from eleven different angles (see this ADR's
"Register" section's table for the full list with bead ids).

Task 2.0's mandate directs this docket to derive the Phase 5 plan once the queue-core phases close,
and this docket's Task 5.0 packet fixes the **minimum contract** that plan and this ADR must pin —
reproduced in full across this ADR's Decision, from "New module" through "Register," below — while
leaving one question open for this ADR itself to decide: **does the `command` role's executor stay
in-core as a generic backing-command path, or does it move out with everything else?** (the
Decision's "Open question resolved" section.)

**The state to extract, concretely.** The move-list packages, read for this ADR
(`packages/pr-pool/internal/{ccpool,watchdog,budget,prompt,complete,beads,prpoolacl}`,
`internal/roles/builtin.go`, `internal/query/beads.go`, `cmd/pr-pool/{sessions_cmd,reconcile_cmd}.go`):

- `internal/roles.Role` carries `Type string // "ccpool" | "command"` and a matching config block
  (`CCPoolConfig` / `CommandConfig`); `internal/executor.For(roleType)` selects
  `ccpoolExecutor{}` or `commandExecutor{}` by that string — the exact shape `GOAL-MIN-1`'s
  minimality clause forbids ("adding a participant is configuration and MUST NOT require changing
  the core; the core makes a single opaque invocation and never distinguishes source or handler
  kinds"), tracked as register row R15 (bead `pg2-goxjh`).
- `commandExecutor` (`internal/executor/command.go`) is the smaller of the two: it renders a
  configured `argv` through `internal/prompt`'s template engine and execs it once via
  `Deps.commander()`, mapping exit `9` to `ErrBusy`. It has no `ccpool`/`beads`/`watchdog`
  coupling of its own — it is pass-through in exactly the sense this set's README's boundary
  principle already blesses ("a `command` handler's argv... looks like another application's
  configuration living inside pr-pool's, but pr-pool invokes it and never reads it, so it is a
  pass-through and belongs"). Its only compiled-in coupling to a package this contract moves is
  `internal/prompt` (argv-template rendering).
- `internal/config.Config.PermissionMode` is validated against `validPermissionModes`
  (`internal/config/config.go:373-421`) — a duplicated copy of `claude`'s own permission-mode
  enum, enforced as a blocking config error — the Floor's tool-naming clause violated a second way
  (register row R16, bead `pg2-vl05m`, alongside the `bd`/`pg-pr`/`ccpool`/`claude` names in
  `--help` and `internal/config/config.go`'s built-in defaults).
- `internal/discover.ToQueueEvent`/`ItemFromPayload` (`discover.go:63-127`) pack/unpack
  `item.Item` fields under `payload["item"]` and write `payload["source"]` — the core-side bridge
  reading and naming payload paths no binding declares (`GOAL-MIN-1`'s payload-opacity row, bead
  `pg2-4t5ey`).
- `internal/complete` (`DoneSignal`/`OnFailure`) is pr-pool's own work-outcome policy —
  `created`/`closed`/`handed-back` — consulted from `orchestrator.go`'s `buildResult`
  (`INV-WORKFLOW-1`'s row, bead `pg2-ctqo2`): the core computing **work** outcomes where the
  invariant scopes it to **delivery** outcomes.
- `cmd/pr-pool/drain.go`'s `precheck`/`precheckPrefix`/`resolveSelf` run three tool-naming/
  connectivity checks (`bd` unreachable, beads-prefix mismatch, `pg-pr config show` self-login)
  ahead of `INV-WORKFLOW-1`'s closed six-check pre-flight set, outside it (register row R14, bead
  `pg2-d4gvb`).
- `internal/query.Query.Run` (`query.go:68`) is a synchronous in-process call with no tracking id
  or callback, and `internal/roles/builtin.go` pairs a beads-backed built-in query set with the
  built-in role set so a config-less deployment still runs (register rows R2/`pg2-nr1xm` and
  `USECASE-CREATE-SOURCE`/`pg2-u7rzl`).
- `cmd/pr-pool/sessions_cmd.go` opens ccpool's own on-disk session-metadata store directly;
  `cmd/pr-pool/reconcile_cmd.go` reports stranded self-owned feedback cycles and runs the pg-pr
  ACL (`internal/prpoolacl`) — both deployment-coupled subcommands the realization-gap register
  already flags as moving out (the pre-existing `GOAL-MIN-1` "Scope (extent out)" row, bead
  `pg2-ynhr.5` / `pg2-ynhr`, and `INTF-CLI`'s `--json`-coverage row R17, bead `pg2-t5j54`, whose
  residual is narrowed to exactly these two subcommands).

Separately, an **operator ruling superseded** the docket's own originally-planned deprecation
shim for these two subcommands (Phillip Green II, 2026-09-02, recorded in docket `pg2-84o3m`'s
design, Addendum after "Phase gating"; docket now at `pd_rev=2`): pr-pool has no live consumers, so
`sessions`/`reconcile` are **deleted outright** in the same change that lands the extraction — no
exit-2 diagnostic stub, no stderr discriminator line, no one-release grace period, no
`MIGRATION.md` entry for this specific removal. A caller still invoking either name gets the
ordinary unknown-subcommand usage error (exit `2` per `ADR 0042`), the same as any other name this
binary has never had.

## Decision

### 1. New module: `packages/prpool-ccpool-handler`

A new Go module, sibling to `packages/pr-pool`, owns every concrete participant implementation
this ADR moves. It:

- imports `packages/pr-pool`'s interfaces (the wire schemas and `conformance` package) rather than
  the reverse — pr-pool's own module MUST NOT import this module, which is what makes the core
  generic again;
- carries its **own** behavior-docs set (`packages/prpool-ccpool-handler/docs/behavior/`),
  because it is now itself an implementer of `INTF-HANDLER`/`INTF-SOURCE` and the concrete
  participant behavior it realizes (ccpool session lifecycle, watchdog/budget policy, beads
  read/write) is exactly the kind of "downstream deployment set" content this set's own
  `## Scope`'s "Extent (out)" already excludes from `packages/pr-pool/docs/behavior/`;
- carries its **own** `gomod2nix.toml`, `flake.nix` package + check attributes, and (per the
  repo's package-versioning convention) its own per-source-digest `--version`;
- carries its **own** `prpool-ccpool-handler-go-tests` check with its **own** coverage-thresholds
  file and gate. This module's thresholds are **never** added to
  `packages/pr-pool/tests/coverage-thresholds.txt` — the module boundary is also the coverage-gate
  boundary, so a change on one side never silently moves the other side's ratchet.

### 2. Move list

The following packages and subcommands move from `packages/pr-pool` into
`packages/prpool-ccpool-handler`, verbatim per the docket's fixed contract:

`internal/ccpool` (the executor), `internal/watchdog`, `internal/budget`, `internal/prompt`,
`internal/complete`, `internal/beads` (the beads client), `internal/prpoolacl`, the built-in
beads-shaped roles and query set (`internal/roles/builtin.go`'s beads defaults and
`internal/query/beads.go`), and the `sessions`/`reconcile` subcommands' **logic**
(`cmd/pr-pool/sessions_cmd.go`, `cmd/pr-pool/reconcile_cmd.go`, `internal/reconcile`) — though per
the superseding operator ruling above, that logic moves as a **deleted** capability, not a ported
one: nothing in the new module is obligated to reimplement `sessions`/`reconcile`'s behavior,
because no live consumer needs it. What actually moves for this pair is the ccpool
session-metadata read path (`sessionmeta`) and the pg-pr ACL (`internal/prpoolacl`) themselves,
insofar as anything downstream of the extraction still wants to build a replacement source/role on
top of them (`push-inject`, not a subcommand) — the docket's design already named that shape and
this ADR does not narrow it further.

`internal/config`'s `CCPoolConfig`/`Budget`/`PermissionMode`/`AllowedTools` fields and their
validation move with their owning package: the core's own `Config` keeps only what is generic
(`RepoRoot`, `WorktreeDir`, poll/gate scalars, the role set as an opaque `Binds` + wire-addressed
handler list) — see the "Wire contract" section's permission-mode bullet for the concrete change
this forces.

### 3. Open question resolved: the `command` executor moves too

**Decision: the generic `command` executor moves out with everything else.** After extraction,
every configured role — whether backed by `ccpool` or by a bare configured command — is dispatched
identically: the core sends `handler.dispatch` over the wire to a registered handler participant,
and it is that participant's **own** business which kind of role it is. `internal/roles.Role`
loses its `Type` field **entirely** — not narrowed to distinguish "wire-dispatched" from "still
in-core" — because a residual in-core `Type` value is precisely the register row R15 finding
restated at one bit narrower rather than resolved.

**Why, stated against the fixed contract's own text.** The wire-contract bullet this docket already
fixed reads "roles.Role loses the Type enum" with no qualifier. A `command` role kept in-core would
force `Role` to retain some Type-shaped field to tell the core "run this argv yourself" from "hand
this to a wire participant" — falsifying that bullet rather than realizing it. `ADR 0061`'s own
framing reinforces the same reading: "nothing before Phase 5 should invent a partial or ad hoc
messaging shim," and a permanent in-core exec path is exactly a permanent partial shim — one
handler kind that never becomes a message, forever, rather than a phase-ordering deferral. Keeping
it would mean Phase 5 completes the message-passing target for `ccpool` roles only, leaving
`command` roles as a structural, undocumented-as-such second class.

**R15 / Floor consequences of each branch:**

|                                                                     | Keep `command` in-core (rejected)                                                                                                                                                                                                                                                                                                                           | Move `command` out too (decided)                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **R15** (`GOAL-MIN-1` minimality, "core never distinguishes kinds") | **Not resolved.** The core retains exactly one hardcoded, zero-registration participant kind and one bit of kind-branching in `executor.For`; `roles.Role` cannot fully drop `Type`, and this ADR would have to narrow the fixed contract's own "loses the Type enum" bullet to "loses it except for `command`."                                            | **Fully resolved.** The core has exactly one handler-dispatch path (`handler.dispatch`) for every role regardless of kind; adding a third, fourth, ... handler kind is pure configuration plus a downstream handler-side capability, never a core change.                                                                                                                                                                                                   |
| **R16** (Floor, "names no concrete tool")                           | Unaffected either way on this specific row: `commandExecutor` never names a concrete tool today (it execs whatever argv the operator configures) — its cost is on the minimality axis (R15), not the tool-naming axis.                                                                                                                                      | Same — moving it doesn't change what R16 already measured elsewhere (`bd`/`pg-pr`/`ccpool`/`claude` names in config/help text), which this ADR's other moves (beads client, `PermissionMode`, `AllowedTools` defaults) already close.                                                                                                                                                                                                                       |
| **Coupling cost**                                                   | `commandExecutor` calls `internal/prompt` for argv templating; `prompt` is on the move list. Keeping `command` in-core while moving `prompt` forces either a forked/duplicated templating helper in-core, or `packages/pr-pool` importing `packages/prpool-ccpool-handler` back — inverting the one-way import direction the "New module" section requires. | No such inversion: `prompt` moves wholesale with the role kind that uses it; the core needs no template-rendering capability of its own at all.                                                                                                                                                                                                                                                                                                             |
| **Operational cost**                                                | None beyond today: a trivial pass-through role (e.g. a health-probe `command` role) needs no separate handler process running.                                                                                                                                                                                                                              | A deployment's simplest role now needs the handler module registered and reachable, even for a one-line `argv` — new standing process surface where there was none. This is the real price of the decision, and it is exactly what the pinned acceptance test "perf baselines re-measured (no daemon regression)" and the `MIGRATION.md` "hard `INV-WORKFLOW-1` check-5 startup failure" note (the "Operator surface" section) exist to catch and disclose. |

The extracted module keeps supporting a `command`-shaped role internally (its own role model MAY
still distinguish "run ccpool" from "run this argv" — that distinction is now the **handler's**
concern, which `GOAL-MIN-1` never governed in the first place: the rule binds the generic core, not
a downstream participant implementation, per this set's `## Scope`'s "Extent (out)").

### 4. No deprecation shim for `sessions`/`reconcile`

Per the superseding operator ruling (2026-09-02): both subcommands are **deleted outright** in the
commit that lands this extraction. No `exitPrecheck`-style diagnostic stub, no dedicated stderr
line, no grace period, no `MIGRATION.md` entry naming this specific removal — an invocation of
either name after this lands gets the binary's ordinary unknown-subcommand usage error (exit `2`,
`ADR 0042`).

### 5. Wire contract

- The handler participant **registers** (`kind: "handler"`), **self-reports**, and receives
  `handler.dispatch` / `handler.dispatch-reply` over the socket under Task 2.2's tracking-id form
  (`dsp-<12 hex>` from `crypto/rand`, minted per offer; a re-offer mints a fresh id) and its
  deferred-form doc comment discipline ("accept is not settle" — a deferred ack is the acceptance;
  nothing further is owed for that dispatch, per `interfaces.md`'s common contract).
- **Work-outcome computation leaves the core.** `internal/complete`'s policy moves with it; the
  dispatch reply's outcome is an **opaque string** the core stores and later exposes (e.g. through
  `status`/activity) without interpreting — closing register row R20 (`pg2-ctqo2`) for real, not by
  relocating the same interpretation into a different in-core file.
- **`roles.Role` loses `Type`** entirely (the "Open question resolved" section). What remains
  in-core is `Name`, `Enabled`,
  `Binds`, and `RetryBackoff` — enough to validate wiring and drive the offer/re-offer/backoff
  machinery, nothing that names a participant kind.
- **Permission-mode passthrough.** `internal/config.validPermissionModes` and its blocking
  `Validate()` check are deleted; `PermissionMode` (if the core keeps the field at all, to display
  it) is carried as an opaque string the core never rejects — the duplicated `claude` enum becomes
  the handler module's own concern (it already needs to know `claude`'s real enum to invoke it).
- **Payload-path opacity is fixed.** `internal/discover.ToQueueEvent`/`ItemFromPayload`'s
  `payload["item"]` packing/unpacking moves to the handler side: the core carries whatever opaque
  payload object a source emitted, verbatim, to the handler that accepts it; the **item shape
  becomes the handler's own contract**, not a core-known struct. This closes the `GOAL-MIN-1`
  opacity row (`pg2-4t5ey`) rather than narrowing it.

### 6. Source-side boundary

`query` becomes a real message: a tracking id, a callback, and a deferred form, matching
`INTF-SOURCE`'s already-documented contract — `internal/query.Query.Run`'s in-process call is
replaced by a query that crosses to a registered source participant exactly as `handler.dispatch`
crosses to a handler. The built-in beads-backed query set (`internal/query/beads.go`,
`internal/roles/builtin.go`'s built-in defaults) moves out with it: **the core ships with no
built-in role or query set once this lands.** A deployment relying on zero-config defaults today
(no `.pr-pool/config.toml`, "using built-in roles" in the log) MUST author real configuration and
run the handler module after this change — recorded as a `MIGRATION.md` consequence in the
"Operator surface" section, not softened here.

The three R14 tool-naming/connectivity startup blockers (`bd` unreachable, beads-prefix mismatch,
`pg-pr` self-login) move to the handler/deployment layer with the beads client that backs them —
after the move, `cmd/pr-pool/drain.go`'s own pre-flight returns to exactly `INV-WORKFLOW-1`'s
closed six-check set, closing register row R14 (`pg2-d4gvb`) by removing the extra checks rather
than documenting them.

### 7. Acceptance (tests, not notes)

Pinned verbatim by the docket's fixed contract; the derived Phase 5 plan (Task 5.11) turns each
into concrete test names and mechanisms:

- A gated core with one deferred session outstanding reports `sessionsInFlight >= 1` and the
  status/TUI banner reads **halted**, not quiescent.
- The handler module imports the `conformance` package's driver and passes the `INTF-HANDLER`
  invoking check **against itself** — the same conformance suite `INV-INTF-2` already requires,
  now exercised by a real out-of-process implementer rather than only the reference harness.
- Perf baselines are re-measured; the extraction introduces a real wire hop where there was an
  in-process call, and this obligation exists precisely to catch a daemon regression from that,
  not to rubber-stamp it.
- An inter-conformance pass runs over the new module ↔ `pr-pool` seam (the
  `behavior-docs-inter-conformance` skill's mode, checking the two sets agree at the boundary each
  now separately documents).

### 8. Operator surface

- `packages/pr-pool/MIGRATION.md` gains an entry for: the new package and the module split itself;
  that the handler binary must now be on `PATH` and running/registered — an unmigrated upgrade (old
  config, no handler module deployed) is a **hard `INV-WORKFLOW-1` check-5 startup failure**, not a
  degraded-but-working state, because the core now has no built-in role/query set to fall back to
  (the "Source-side boundary" section); the `[[role]].type` field's before/after shape; and the
  moved subcommands (`sessions`/`reconcile` are **removed**, not moved — no migration path is owed
  for them per the superseding ruling, the "No deprecation shim for `sessions`/`reconcile`"
  section).
- `home/programs/pr-pool` and `darwin/modules/pr-pool` are updated for the split; the new module
  gets its own home-manager capability (`home/programs/prpool-ccpool-handler/`) and a
  systemd(user)/LaunchAgent unit that registers with a running `pr-pool` core, mirroring the
  existing `periodicDrain`/`daemon` pattern in `home/programs/pr-pool/default.nix` and its
  `darwin/modules/pr-pool` LaunchAgent mirror.
- The downstream deployment set (`your-private-flake · modules/zm/pr-pool`) is **flagged**, not
  migrated by this ADR or this docket — it is out of this repo and out of this ADR's authority; the
  derived plan's operator-surface task records the flag as a note to raise with that deployment,
  not a build item here.

### 9. Register

Delete, in the same change that lands the extraction:

| Row                                                       | Bead                      | Why it closes (not narrows)                                                                                                                                                                                                                                                                                                                                                                                                                            |
| --------------------------------------------------------- | ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `INTF-HANDLER` remainder                                  | `pg2-q6tqg`               | `handler.dispatch` crosses a real wire; the "still a Go method call" residual is gone.                                                                                                                                                                                                                                                                                                                                                                 |
| `INTF-SOURCE` message boundary                            | `pg2-nr1xm`               | `query` crosses a real wire (the "Source-side boundary" section); no in-process shortcut remains on either side of `INTF-SOURCE`/`INTF-HANDLER`.                                                                                                                                                                                                                                                                                                       |
| `USECASE-CREATE-SOURCE` deferred-shape                    | `pg2-u7rzl`               | Both `query` and `dispatch` now have a genuine deferred-reply code path once a message actually crosses.                                                                                                                                                                                                                                                                                                                                               |
| `INV-WORKFLOW-1` work-outcome                             | `pg2-ctqo2`               | `internal/complete` moves out; the core stores outcome verbs opaquely (the "Wire contract" section).                                                                                                                                                                                                                                                                                                                                                   |
| `GOAL-MIN-1` payload opacity                              | `pg2-4t5ey`               | The core no longer reads or writes any named payload path (the "Wire contract" section).                                                                                                                                                                                                                                                                                                                                                               |
| `GOAL-MIN-1` participant implementations (R15)            | `pg2-goxjh`               | The core makes one opaque handler-dispatch call regardless of kind (the "Open question resolved" and "Wire contract" sections).                                                                                                                                                                                                                                                                                                                        |
| `GOAL-MIN-1` Floor tool-naming (R16)                      | `pg2-vl05m`               | `bd`/`pg-pr`/`ccpool` compiled-in names and the duplicated `claude` permission-mode enum move with their owning packages (the "Move list" and "Wire contract" sections).                                                                                                                                                                                                                                                                               |
| `INV-WORKFLOW-1` R14 startup blockers                     | `pg2-d4gvb`               | The three extra pre-flight checks move to the handler/deployment layer (the "Source-side boundary" section); the core's own pre-flight returns to its documented closed six.                                                                                                                                                                                                                                                                           |
| `INTF-CLI` R17 residual (`sessions`/`reconcile` `--json`) | `pg2-t5j54`               | The residual was scoped to exactly these two subcommands; they are deleted (the "No deprecation shim for `sessions`/`reconcile`" section), so there is no remaining gap to hold open.                                                                                                                                                                                                                                                                  |
| `INV-EVT-2` duplicate-absorption                          | `pg2-v0hhx`               | **Requires real build work, not relocation alone** — see note below.                                                                                                                                                                                                                                                                                                                                                                                   |
| `GOAL-MIN-1` "Scope (extent out)" — `reconcile`           | `pg2-ynhr.5` / `pg2-ynhr` | The intended sync-as-a-source-or-role behind `push-inject` is **obsoleted**, not built: no live consumer needs it, so the row closes because the need it named no longer exists (the "No deprecation shim for `sessions`/`reconcile`" section), not because Phase 5 built the thing it described. This closure is recorded as obsoleted-not-realized in the plan's register-update task, so a later reader does not mistake it for delivered behavior. |

Floor is re-checked as part of closing R16: after the "Move list" and "Wire contract" sections' moves, `packages/pr-pool`'s own contract
surface (`--help`, config validation, built-in defaults) MUST name no concrete tool at all before
this row is deleted, not merely fewer tools than before.

**`INV-EVT-2` needs a real fix, not just a new address.** The register's finding
(`internal/orchestrator/orchestrator.go:63-71`) is that a crash-window redelivery starts a **second
fresh session** because each attempt mints a new per-attempt stamp (`Role.ExternalID`) with no
memory of the prior attempt. Moving the executor out from behind a wire boundary does not, by
itself, give the handler that memory — the dispatch tracking id is deliberately fresh per offer
(Task 2.2's own doc comment: "re-offer mints a fresh id"), so it cannot be the correlation key for
this. The derived plan MUST include a task giving the extracted handler duplicate-absorption logic
keyed on stable role/item identity (e.g. `Role.DisplayName`, which is already stable per bead
rather than per attempt) before this row is deleted; deleting it as a side effect of the move
without that logic would misrecord an unrealized gap as closed.

## Consequences

### Positive

- The generic binary (`packages/pr-pool`) becomes what its own behavior docs already describe: a
  dispatcher that knows events, bindings, participants, handler sessions, and wiring, and nothing
  about `bd`, `ccpool`, `claude`, or any other concrete tool. Ten of the eleven register rows this
  ADR-and-plan pair close were already-recorded, already-triaged intent; this is exactly what the
  register exists to let happen without a surprise on either side.
- `roles.Role` losing `Type` outright (not narrowed) removes the single biggest structural reason
  adding a new handler kind has ever required a core change.
- The module boundary's own coverage gate (the "New module" section) means the new module's test debt can never silently
  borrow against `pr-pool`'s own ratchet, and vice versa.

### Negative

- Every dispatch — including what is today a zero-dependency, in-process `exec.Command` for a
  trivial `command` role — now pays a real wire hop and requires a running, registered handler
  process. This is a genuine operational regression risk for the simplest deployments, which is why
  perf re-measurement is a pinned acceptance test (the "Acceptance" section) rather than a nice-to-have.
- The core shipping with no built-in role/query set (the "Source-side boundary" section) is a
  breaking change for any zero-config deployment; `MIGRATION.md`'s hard-startup-failure note (the
  "Operator surface" section) is the disclosure mechanism, not a softening of the break.
- `INV-EVT-2`'s closure (the "Register" section) is gated on new handler-side logic this ADR specifies but does not
  build; a Phase 5 that deletes the register row before landing that logic would misrecord the
  gap as closed.

### Neutral

- This ADR builds nothing itself; it authorizes the derived Phase 5 plan (see "Derived plan"
  below) and the OQ bead this ADR resolves (see "OQ bead" below).
- The downstream deployment set's own migration is flagged, not scheduled, by this ADR — it has no
  authority over `your-private-flake`.

## Alternatives Considered

### Keep the `command` executor in-core as a permanent generic fallback

Rejected — see the "Open question resolved" section's table. The fixed contract's own "roles.Role loses the Type enum"
bullet, read literally, already points away from this; the coupling cost (`internal/prompt`
moving out from under an in-core caller) makes it worse in practice, not just in principle.

### Port `sessions`/`reconcile` behind `push-inject` instead of deleting them

This was the docket's **original** plan (a deprecation shim, then a downstream source/role
replacement). Superseded by the 2026-09-02 operator ruling recorded in Context: no live consumer
exists, so there is nothing to port. Rejected as unnecessary work, not as a wrong design — if a
consumer appears later, the shape this ADR's Context describes (a source or role behind
`push-inject`) is still the right one to build then.

### Split the new module into two — a `ccpool`-specific handler and a generic command-runner

Considered, given the "Open question resolved" section's outcome makes the module host two
genuinely different role kinds internally. Rejected for this pass: the docket's fixed contract pins
the module's name and existence (`packages/prpool-ccpool-handler`) as the minimum this ADR must
realize, and splitting it further is additional structure the fixed contract does not ask for and
the OQ's resolution does not require — the module's own internal role model (the "Open question
resolved" section's closing paragraph) can hold both
kinds without a second Go module. Nothing here forecloses a future split if the module grows
unwieldy; it is simply not this ADR's decision to make preemptively.

## Operator review

This ADR's own acceptance criteria (docket `pg2-84o3m`, Task 5.0) name **operator review of this
ADR, recorded before Phase 5 execution begins**, as a required step this ADR cannot itself
satisfy — it is a human action, not a drafting one. This ADR is filed as **Proposed** for exactly
that reason; the docket packet marks the derive-plan work `done` on the understanding that this
review is the one remaining step before the derived plan below may be executed.

## Related Decisions

- Realizes `ADR 0061`'s Decision, "Participant model: full message-passing is the target; the
  in-process shortcut is register-held intent" (the participant-model target) by specifying the
  concrete extraction that decision authorized for Phase 5 and no earlier task.
- Consumes `ADR 0042` (coarse exit-code convention) for the `sessions`/`reconcile` deletion's exit
  behavior (the "No deprecation shim for `sessions`/`reconcile`" section) and for the dispatch
  reply's continued use of exit `9` (busy) — unchanged by this ADR.
- Consumes `ADR 0036` (a CLI never auto-starts a core) — unaffected by this extraction; the
  handler module registers with an already-running core, it does not start one.
- The command-executor open question (the "Open question resolved" section) is filed and resolved
  as bead `pg2-mbm5i`.
- Design spec:
  `docs/superpowers/specs/2026-08-29-pr-pool-impl-conformance-delta.md` (Theme A/B for the
  participant-interface and minimality findings this ADR closes; that report's own "Register
  reconciliation and human decisions" section, "Decisions only a human can make" item 3, already
  answered by `ADR 0061`, which this ADR builds on).
- Realization-gap register: `packages/pr-pool/docs/behavior/README.md`'s "Realization gaps"
  section, this ADR's own "Register" section's table.
