# pa-monitor: registry-driven session activity + honest caffeinate state

**Status**: Draft — reviewed 2026-06-18 (subagent, against source); revised. See §13.
**Date**: 2026-06-18
**Deciders**: Phillip Green II
**Beads**: epic `pg2-oois` → `pg2-oois.2` (shared lib), `pg2-oois.3` (pa-monitor fix), `pg2-oois.4` (caffeinate indicators), `pg2-oois.5` (ccpool adopt, low-pri); related `pg2-u7lf` (scheduled-resume detection)

---

## 1. Context & problem

### The incident (2026-06-18)

An overnight interactive session named `add-sox-alerts` (`/Volumes/ziprecruiter/pristine`, pid 5309) hit
`API Error: The socket connection was closed unexpectedly` and sat un-recovered for ~3h18m until the user
woke the Mac and resumed it by hand. pa-monitor never nudged it.

### Root-cause chain (verified)

1. pa-monitor decides a session's working/idle state purely from the **main** transcript file's mtime
   (`session.go:82-92` `Classify`, `WorkingThreshold` 30s), via `ResolveTranscript`
   (`session/transcript.go`) which **skips subdirectories** — so `<sessionid>/subagents/agent-*.jsonl`
   never counts.
2. The session spent the night orchestrating subagents (a 74-min `general-purpose` Agent run). The **main**
   transcript was therefore quiet, so pa-monitor classified the session Idle/Dormant.
3. Caffeinate is gated on `anyWorking` (`lifecycle.go:391-397`). With no session "working", the caffeinate
   assertion lapsed (`caffeinate/manager.go` `Tick`).
4. The Mac (lid open, on AC) idle-slept. Sleep **severed the in-flight API socket** (both errors land on
   dark-wake/sleep transitions) **and** suspended the pa-monitor daemon, so no poll/nudge could fire.
5. By the time the user woke the machine, they resumed manually within ~43s — before the daemon got a clean
   awake-and-still-disrupted tick.

### The miss

Claude Code itself maintains an authoritative, real-time-ish status in `~/.claude/sessions/<pid>.json`:
`status` ∈ {`busy`,`idle`,`waiting`} + `waitingFor` (e.g. `"permission prompt"`) + `statusUpdatedAt`.
pa-monitor **already opens this file** (`session/discovery.go:46-89`) but `rawSession` (`discovery.go:11-19`)
decodes only `pid/sessionId/cwd/kind/entrypoint/name/startedAt` and **discards `status`/`waitingFor`/
`statusUpdatedAt`**. The harness sets `busy` for the whole turn — _including subagent and background work_ —
so consuming it closes the blind spot that caused the incident.

---

## 2. Goals / non-goals

**Goals**

- A session is considered active if **any** agent (main, subagent, or background task) is working — derived
  from signals Claude Code already emits, not CPU.
- Keep the Mac awake while real work is happening (fixing the sleep-mid-work → socket-drop cause).
- After a disrupt, stay awake long enough to _attempt_ recovery once.
- Distinguish "blocked on a human" (don't keep awake, don't nudge) from "working" and "idle".
- Make caffeinate state **unambiguous and observable**: separate "auto mode" from "the process actually
  holding the assertion", across TUI / OTel / CLI.

**Non-goals / out of scope (explicit decisions)**

- ccpool-as-a-3rd-wrapper: no ccpool status API, no `ccpool reply` delivery, no ccpool-specific signaler.
  ccpool's existing nudge opt-out is sufficient for now.
- A "force awake" caffeinate mode — auto mode is fine.
- CPU-based busy detection — rejected (a network/API-blocked turn is busy at ~0% CPU).
- Keeping the Mac awake until a **scheduled** resume (ScheduleWakeup/loop/cron) fires — deferred; we only
  _detect/surface_ scheduled-resume intent (`pg2-u7lf`).
- Working with the lid closed or off AC — accepted limitation of `caffeinate`.

---

## 3. Decisions (summary)

| #   | Decision                                                                                                                                                                                                                                                                                                                                                              |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | Session activity is driven by the registry `status` field (pid-alive gated), **not** transcript mtime.                                                                                                                                                                                                                                                                |
| D2  | `busy` (pid-alive) is **trusted** and never demoted on transcript staleness — a `busy` main transcript is _expected_ to be stale during a subagent run, and demoting it reintroduces the bug (esp. for resumed sessions where the subagents dir won't resolve). Transcript mtime is a freshness cross-check for **`waiting` only**; `busy`/`idle` need none.          |
| D3  | New first-class **waiting-for-human** state suppresses **both** caffeinate and nudges.                                                                                                                                                                                                                                                                                |
| D4  | Registry `waiting` is **not trusted raw** (it goes stale — observed 16h); cross-check freshness + corroborate AskUserQuestion via `IsAwaitingInput`.                                                                                                                                                                                                                  |
| D5  | **Error keep-awake** (awake-only safety net): a terminal _nudgeable_ error with **zero recorded nudge attempts** holds caffeinate until the first attempt (a _failed_ attempt counts), then releases. `LastError`-based, computed from tree+watermark independently of the nudger (which ticks later). Does NOT rescue an already-suspended daemon — that's D1's job. |
| D6  | Caffeinate exposes **two** indicators: auto-mode (`on`/`off`) and process (`off`/`on`/`grace`/`error`).                                                                                                                                                                                                                                                               |
| D7  | The registry reader + freshness helper live in the shared `claude-transcript` library; pa-monitor and (later, low-pri) ccpool consume it.                                                                                                                                                                                                                             |

---

## 4. The session-activity model

### 4.1 Inputs (per session, per tick)

- **Registry row** `{status, waitingFor, statusUpdatedAt}` from `~/.claude/sessions/<pid>.json` (new fields
  on `rawSession`).
- **PID liveness** — `DefaultPidAlive` (`kill -0`, `discovery.go:30-39`); already computed.
- **Transcript freshness** — `lastActivity` = MAX modification time over the main transcript **and** every
  `<sessionid>/subagents/agent-*.jsonl`. (Today `ResolveTranscript` ignores subagents — this is the fix.)
- **`IsAwaitingInput(path)`** — shared lib (`claude-transcript/awaiting.go`); dangling AskUserQuestion.
- **`LastAPIError(path)` / `RetryClass()`** — shared lib (`claude-transcript/apierror.go`); terminal/retryable error.

### 4.2 Verdict algorithm

```
if !PidAlive(pid):            -> keep last-known (poller persists until GC); not active
lastActivity = max(mtime(main), max mtime over subagents/agent-*.jsonl)

# waiting-for-human (suppress caffeinate + nudge)
if (status == "waiting" AND fresh(statusUpdatedAt, lastActivity)) OR IsAwaitingInput(main):
    state   = WaitingForHuman
    reason  = waitingFor (if "waiting" & present)            # e.g. "permission prompt"
            | "AskUserQuestion" (if IsAwaitingInput)
            | "plan-review" (if trailing ExitPlanMode tool_use)   # best-effort
            | "unknown"
# active / working — TRUST busy (pid already gated); do NOT demote on transcript staleness
elif status == "busy":   state = Working   # stale main transcript is EXPECTED during subagent runs
# done
else:                    state = Idle       # status=="idle", or a stale "waiting" that fell through

# display-only age bucket, orthogonal to the verdict
ageBucket = Dormant if (now - lastActivity) > IdleThreshold else fresh
```

### 4.3 Freshness rules (D2/D4) — why mtime stays, applied asymmetrically

The registry can lie; mtime is the corroborator — but only where it's safe:

- **`waiting` goes stale** — observed `waiting`/`permission prompt` stuck for **16+ hours** after the human
  moved on (transcript kept advancing). So trust `waiting` only when `statusUpdatedAt` is close to
  `lastActivity`; otherwise ignore the flag (fall through to Idle). **This is the one place mtime gates the
  verdict.**
- **`busy` is TRUSTED — do NOT demote on transcript staleness.** `statusUpdatedAt` is a turn-START marker,
  not a heartbeat (a genuine 16-min turn has a 16-min-old timestamp), and a `busy` session's _main_
  transcript is _expected_ to be stale while a subagent runs. The earlier "demote stale busy → Dormant" idea
  is **rejected** because (a) it directly reintroduces the incident bug, and (b) for **resumed/forked**
  sessions the `<resolvedBase>/subagents` dir doesn't resolve (`claude-transcript apierror.go:240-244`), so
  `lastActivity` would miss the subagent writes and wrongly demote a genuinely-working session — and the
  incident session was resumed. We accept that a hung-but-alive `busy` keeps the Mac awake indefinitely (the
  lesser evil; pid-liveness is the only gate). **Consequence: subagent-mtime is NOT load-bearing for
  keep-awake** — it only feeds the `waiting`-freshness check and the display/age "dormant" bucket.

The `waiting`-freshness helper computes `lastActivity` from real **message** events, filtering trailing
metadata (`mode`, `permission-mode`, `last-prompt`, `custom-title`, `agent-name`, `pr-link`,
`queue-operation`, `turn_duration`) — scan from the end for the last `assistant`/`user` event; file mtime is
an acceptable proxy.

### 4.4 Status enum impact

`session.Status` (`session.go:8-14`) is `Working/Idle/Dormant`. Add **`WaitingForHuman`**. Full ripple
(review-verified — several are easy to miss):

- `Status.String()` + the `aggregate.Build` switch (`aggregate.go:30-37`) + `Directory.WaitingN` (and its DB
  round-trip in `convertDirectory` `state_convert.go:68-78` and `service/tree_builder.go`).
- **DB-materialized parse path (correctness-critical):** `proto/from_proto.go:171 statusFromString` and
  `daemon/state_convert.go:197 parseSessionStatus` both default unknown → **Dormant**. The TUI/CLI/OTel read
  the _DB-materialized_ tree (`state.go:75`), so without adding `waiting` here it silently round-trips to
  Dormant. **No DB migration needed** — the status column is `TEXT NOT NULL DEFAULT ''`, no CHECK constraint.
- proto `SessionView.status` (`proto:127`) string set; OTel `state` label (`lifecycle.go:568`, `:647`).
- TUI `render/tree.go`: glyph `symbol()` (`:164-179` — add a waiting case or it renders as Dormant `✕`);
  the predicates at `:99` (`visibleSessions` `!= Dormant` — waiting stays visible ✓) and `:203`
  (`== Working` — waiting correctly excluded ✓) are fine but should be eyeballed.
- Tests: ~27 files assert the 3-value enum/strings (`session_test.go`, `aggregate_test.go`,
  `session_glyph_test.go`, `dispatcher_test.go`, `tick_integration_test.go`, …).

---

## 5. Caffeinate model

### 5.1 Two indicators (D6)

The `caffeinate_on` toggle does **not** mean "stay awake"; per `manager.go:52-70` it only _arms_
auto-caffeinate, which spawns only while `anyWorking`. The night of the incident the state was
**mode:on + process:off** (armed, not holding) — invisible today because `lifecycle.go:410` collapses
everything to a single `active bool`. Replace with two explicit, separately-surfaced indicators:

| Indicator                | Values                                     | Source                                                                                                                           |
| ------------------------ | ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| **Auto-caffeinate mode** | `on` / `off`                               | the toggle (`system_toggles.caffeinate_on`; read via `state.isCaffeinateOn()`)                                                   |
| **Caffeination process** | `off` / `on` (holding) / `grace` / `error` | `Manager.State()` (`StateOff`→`off`, `StateArmedRunning`→`on`, `StateArmedCountdown`→`grace`) + a new `error` when `Spawn` fails |

`error`: `manager.go:69` currently discards `Spawn`'s error (`_ = m.Spawn(...)`). Capture it; surface
`process=error` so a broken caffeinate is visible rather than silently "off".

### 5.2 Awake triggers (replaces bare `anyWorking`)

`Manager.Tick(keepAwake)` where:

```
keepAwake = anyWorking                          # any session state == Working (D1/D2)
          OR anyUnattemptedNudgeableDisrupt      # D5; see §5.3
```

WaitingForHuman / Idle / Dormant do **not** contribute (D3). **Both disjuncts are computed inline at
caffeinate-tick time from `tree` + the `WatermarkStore` — NOT from the nudger's pending-store** — because the
nudger reconciles _later_ in the same tick (`lifecycle.go:466` runs after the caffeinate driver at `:398`),
so its pending/attempt state for this tick isn't available yet at `:402`. Also update the no-poller branch
(`lifecycle.go:343-347`), which calls `Tick(false)`.

### 5.3 Error keep-awake (D5)

While the Mac is awake, a session whose `LastError` is terminal + **nudgeable** with **zero recorded nudge
attempts** keeps caffeinate held until the first attempt, then releases to grace.

- **Predicate is `LastError`-based, not pending-store-based.** It must be true _immediately_ when the error
  is observed (T+0) — i.e. before the nudger's `DisruptGrace` (30s) elapses and enqueues anything — or the
  Mac could idle-sleep during the grace, before the first attempt. Compute from `tree[*].LastError` + the
  watermark, independent of the nudger.
- **nudgeable** = `transcript.Retryable(LastError)` (`RetryClass ∈ {TransientServer, TransientNetwork}`) AND
  `signal.ResolveSignaler` returns non-nil for the pid AND `!LastError.FromSubagent` AND not ccpool-opted-out.
- **attempt counts even on failure.** Today the dispatcher records only on success (`dispatcher.go:129-130`
  `UpdateWatermarks`) and silently `continue`s on `Send` error (`dispatcher.go:111-113`). Add an **attempt
  watermark recorded on both paths**, persisted in `runtime.json` (mirror `LastDisruptNudgeAt`).
- **Persisted-attempt is the keep-awake authority; the in-memory grace clock (`firstSeen`) is separate.**
  After a restart/suspend, `firstSeen` resets (re-graces) but the persisted attempt does not — and that's
  correct: zero-attempts ⇒ hold awake (through the fresh grace); attempt-recorded ⇒ release (retries don't
  re-hold). Consistent _for the keep-awake purpose_.

**Scope/limits (honest):** D5 is an **awake-only safety net**. It cannot rescue a session if the Mac has
_already_ slept and suspended the daemon (no tick fires) — that is exactly what D1 (busy keep-awake) prevents
by not sleeping mid-work. D5 also does nothing for a **subagent-only** terminal error (`FromSubagent` ⇒
excluded from both nudge and keep-awake) — a latent gap; see §11.

---

## 6. Nudge interactions

- **WaitingForHuman suppresses nudges** (D3) — never inject `"continue"` over a permission prompt /
  AskUserQuestion.
- Existing `session_active` suppression (`dispatcher.go:105`, suppress when `status == Working`) is unchanged
  and now keyed on the registry-derived Working.
- The disrupt producer (`nudger/disrupt.go reconcileSession`) gates unchanged
  (AutoResumeEnabled → LastError terminal → Retryable → !FromSubagent → grace); D5 only adds the
  _attempt-recording_ and the _caffeinate_ trigger, not new nudge gates.

---

## 7. Shared library: `claude-transcript` (pg2-oois.2)

Add registry-oriented entry points alongside the existing transcript-path primitives:

- `ReadSessionRegistry(sessionsDir) []RegistrySession` and/or `SessionStatus(sessionsDir, sessionID)` —
  parse `pid, sessionId, cwd, name, kind, entrypoint, startedAt, status, waitingFor, statusUpdatedAt`.
- A **normalized verdict** type — proposed `active` / `idle` / `waiting-for-human` — with a documented
  mapping from `(status, waitingFor, freshness, IsAwaitingInput)`.
- A **pid-liveness** helper and a **freshness** helper (`statusUpdatedAt` vs last transcript message-event).
- Keep `IsAwaitingInput` as the AskUserQuestion detector.
- **Caveats to document in the library:** `waiting` can be hours-stale; `statusUpdatedAt` is turn-start (not
  a heartbeat); `waitingFor` only `"permission prompt"` observed (field since CC 2.1.162); registry is keyed
  by **PID** (stale `busy` survives a crash → always pid-gate).
- `pg2-u7lf`'s scheduled-resume detector co-locates here (advisory; surfaced, not acted on).

Both pa-monitor and ccpool (later, `pg2-oois.5`) consume this so they agree on the jsonl/registry-derivable
verdict. ccpool keeps its own pane-derived sub-state (thinking/streaming), which is not reproducible from
the registry.

---

## 8. pa-monitor changes (file-by-file)

- `internal/core/session/discovery.go` — `rawSession` gains `status`,`waitingFor`,`statusUpdatedAt`; thread
  onto `Session`.
- `internal/core/session/session.go` — add `WaitingForHuman` to `Status`; the verdict (§4.2) supersedes raw
  `Classify`-from-mtime as the primary, with `Classify` retained for the age/dormant cross-check.
- `internal/core/session/transcript.go` — `ResolveTranscript` (or a new helper) must expose `lastActivity`
  as **max over main + `subagents/agent-*.jsonl`** (the subagent fix).
- `internal/core/poller/poller.go` — set `Status` from the registry verdict + freshness cross-check
  (`:111-119`); `anyWorking`/return value (`:361`) keys on the new Working; keep the LastSubagentError fold
  (`:196-205`); revisit the Dormant→Idle pid-alive bump (`:267-280`).
- `internal/core/aggregate/aggregate.go` — add `WaitingN`.
- `internal/core/caffeinate/manager.go` + `proc.go` — capture `Spawn` error → `process=error`; `Tick` takes
  `keepAwake` (§5.2).
- `internal/daemon/lifecycle.go` — compute `keepAwake` (anyWorking ∪ pending-unattempted-nudgeable-disrupt);
  stop collapsing caffeinate to a bool (`:410`); store mode + process state.
- `internal/daemon/nudger/dispatcher.go` — record a nudge **attempt** on both success and failure.
- `internal/daemon/nudger_runtime.go` (`WatermarkStore`) — persist the attempt watermark.

---

## 9. Surfaces

- **proto** (`internal/proto/pa_monitor.proto`): `SessionView.status` (`:127`) gains `waiting`; `DaemonState`
  caffeinate becomes mode + process (replace/augment `caffeinate_active` `:85`); populate the long-unused
  `CaffeinateResponse.until` (`:193`) for grace seconds.
- **OTel** (`internal/otel/emitter.go`): `state` label gains `waiting`; `caffeinate.active` → mode gauge +
  process-state attribute (+ optional `caffeinate.grace_remaining_seconds`). OTel transport is healthy
  (grpc/4317 → otelcol-contrib; metrics live in Prometheus), so these land.
- **TUI** (`internal/render/controls.go`, `tui/view.go`): two caffeinate indicators (revive the dead
  `controls.go:53-62` countdown branch — `view.go:31-40` never passes `GraceRemaining`); `render/tree.go`
  glyph gains a waiting symbol.
- **CLI** (`cmd/pa-monitor/cli.go`,`control.go`): `status` shows both caffeinate indicators; per-session
  `status` shows `waiting`.

---

## 10. Out of scope / deferred

- ccpool wrapper / status-API / reply delivery (use existing opt-out).
- Keep-awake-until-scheduled-resume (ScheduleWakeup/loop/cron) — detection only (`pg2-u7lf`).
- Force-awake mode; CPU signal; lid-closed/off-AC operation.
- OTel: no code change needed; optional hygiene = truncate the stale `launchd-stderr.log` (06-04 outage
  flood) and optionally raise `OTEL_METRIC_EXPORT_TIMEOUT`.

---

## 11. Risks & open items

- **Freshness thresholds** (`recentlyAdvanced`, the `waiting`-staleness window) need concrete values; start
  near `WorkingThreshold` (30s) / `IdleThreshold` (10m) and tune.
- **Hung-but-alive `busy`** (pid alive, transcript long-stale): demoted to Dormant → not kept awake.
  Accepted; revisit if it bites.
- **`waitingFor` coverage**: only `"permission prompt"` confirmed; AskUserQuestion covered via
  `IsAwaitingInput`; plan-review best-effort via trailing `ExitPlanMode`; others → "unknown" (still
  suppresses, just unlabeled).
- **Naming (negotiable):** verdict `active/idle/waiting-for-human`; caffeinate process `off/on/grace/error`.
- **Sessions without a registry row** (non-CLI entrypoints, if any) fall back to mtime `Classify`.

---

## 12. Bead mapping

- `pg2-oois.2` — §7 shared-lib registry reader + freshness helper (+ `pg2-u7lf` co-located).
- `pg2-oois.3` — §4, §5.2, §5.3, §6, §8 (the fix). Blocked by `.2`.
- `pg2-oois.4` — §5.1, §9 (two caffeinate indicators incl. `error`).
- `pg2-oois.5` — §7 ccpool adoption (low-pri). Blocked by `.2`.
- `pg2-u7lf` — scheduled-resume detection (advisory).

---

## 13. Review (2026-06-18, subagent against source) — findings & resolutions

Verdict: _ship-with-fixes_. All cited code references verified accurate. Substantive findings, each resolved
above:

1. **Demoting "stale busy" → Dormant reintroduced the bug.** For resumed/forked sessions the subagents dir
   doesn't resolve, so `lastActivity` misses subagent writes and a working session is wrongly demoted — and
   the incident session was resumed. **Resolved:** D2/§4.2/§4.3 now **trust `busy` (never demote)**;
   subagent-mtime is no longer load-bearing for keep-awake (only feeds `waiting`-freshness + display).
2. **D5 ordering/definition bug.** Caffeinate ticks (`lifecycle.go:398`) _before_ the nudger reconciles
   (`:466`), and "pending in store" is empty during the 0–30s grace → Mac could sleep before the first
   attempt. **Resolved:** §5.2/§5.3 redefine the trigger as `LastError`-based (true at T+0), computed inline
   from tree+watermark, independent of the nudger.
3. **New `waiting` status silently round-trips to Dormant** on the DB-materialized tree
   (`from_proto.go:171 statusFromString`, `state_convert.go:197 parseSessionStatus` default→Dormant).
   **Resolved:** added to §4.4 (no DB migration needed — no CHECK constraint).
4. **Grace clock vs persisted attempt after restart/suspend.** **Resolved:** §5.3 clarifies the persisted
   attempt is the keep-awake authority; in-memory `firstSeen` reset is independent and consistent.
5. **Full Status ripple was under-enumerated.** **Resolved:** §4.4 now lists `Status.String()`,
   `aggregate.Build`+`Directory.WaitingN` (+ `convertDirectory`/`tree_builder` DB round-trip), the two parse
   sites, `render/tree.go:99/164-179/203`, the no-poller `Tick(false)` caller (§5.2), and the ~27 enum tests.

Accepted as-is / noted:

- Reuse `SessionEnrichment.AwaitingInput` (already computed each tick at `snapshot.go:211`/`poller.go:214`)
  instead of re-scanning for the AskUserQuestion arm.
- The `IsAwaitingInput` waiting-arm is intentionally **not** freshness-gated (a dangling AskUserQuestion is a
  real unanswered question → WaitingForHuman until answered). Only the registry `waiting` _flag_ is
  freshness-gated.
- `anyWorking` also feeds the TUI summary (`tui/poll.go`,`model.go`) and `rpcclient` — the meaning change
  (registry-driven Working) ripples there too; intended.
- **Honest limitation:** D5 cannot help once the Mac has already slept (daemon suspended); only D1 prevents
  that. And a _subagent-only_ terminal error is excluded from both nudge and keep-awake (latent gap).
