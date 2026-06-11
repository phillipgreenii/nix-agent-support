# ccpool Claude-Code Contract Test Harness — Design

**Status**: Draft (revised after two subagent reviews)
**Date**: 2026-06-11
**Deciders**: Phillip Green II (with Claude)

## Context

ccpool drives the real `claude` REPL through tmux: it sends keystrokes (bracketed
paste, Enter, Escape, C-u), scrapes the pane for markers (e.g. `Interrupted`), and
relies on Claude Code hooks (SessionStart→ready, Stop→done, StopFailure→failed,
Notification(`permission_prompt|idle_prompt`)→needs_input) to learn session state. All
of these are an implicit **contract with the Claude Code TUI**, not with the model.

Runtime verification on 2026-06-11 (bead `pg2-oapb`) found a class of bug the existing
hermetic tests (fake-claude stub) structurally cannot catch — **REPL-contract drift**:

- Cancelling during **streaming** works: Escape yields
  `⎿ Interrupted · What should Claude do instead?`, which `interruptLanded`
  (`internal/session/cancel_close.go:30`) matches → `cancel` exits 0.
- Cancelling during **thinking** does not: a single Escape is a no-op; double-Escape /
  Ctrl-C trigger Claude Code's _rewind/edit-previous-message_ UI (turn discarded, prompt
  restored into the input box) and **no `Interrupted` marker is printed** →
  `interruptLanded` false → `cancel` exits 6 (`cmd/ccpool/cancel.go:51`), row stuck
  `working`. A clean sweep saw **9 of 12 cancels miss**; outcome tracks _phase_, not
  delay. `interruptLanded` also false-positives on a stale `Interrupted` line in pane
  scrollback (substring match, `cancel_close.go:31`).

Hermetic tests pass because the stub (`cmd/ccpool/testdata/fake-claude`) fires `Stop` on
every input line with no phase model. We need tests that drive the **real** `claude`
binary, cheaply and repeatably, so that after a new Claude Code version we can tell
**whether — and where — the contract drifted**.

## Goal

A **locally-run, on-demand** Go test suite that drives the real `claude` binary through
ccpool and pins the Claude-Code contract. Primary use: after a Claude Code upgrade (or a
ccpool change), run it to **detect and localize** contract breakage.

This is **harness-first** and **purely additive** (no product-code changes in v1). Most
deep behavioural assertions need observability ccpool does not yet expose; v1:

1. Builds and proves the harness **mechanics** (isolation, drive real Claude to a target
   phase, timeouts, exit-code capture, deterministic output, `doctor` parsing).
2. Writes the full **catalog** of contract scenarios.
3. Per behaviour: asserts **objective facts**, **pins the currently-observed value as a
   baseline** even when the _desired_ value differs, and leaves a **pending** note where
   nothing is checkable yet (with the observability it needs).

The collected pending notes _become the requirements spec_ for the next phase.

## Non-Goals (v1)

- Not in CI / `nix flake check` / pre-commit (needs real `claude` + OAuth + tmux +
  tokens). Gated behind the `contract` build tag.
- Not a correctness proof of cancel/interrupt — desired-behaviour asserts are pending.
- **No product-code changes** (no `attend.go` testability refactor; no new module deps).
- Does not build the reconciled state query or event log, nor fix the cancel bug.

## Decisions

1. **Harness-first, no hack asserts, purely additive.** Driving may scrape the pane
   (scaffolding); **correctness assertions may not.** Where a real assert needs untrusted
   interpretation, pin the _observed_ value as a baseline and/or leave a pending note.
2. **Runner: Go, behind `//go:build contract`.** Reuses `runCC`/`buildCCPool`/fake-claude
   patterns from the existing integration tests; excluded from `nix flake check` by the
   tag. Runs **serially** (`-p 1`, no `t.Parallel`) with the **suite timeout disabled**
   (`-timeout=0`) plus per-test budgets — real thinking turns are ~25–40s each and the
   default 600s `go test` timeout would SIGQUIT the run. Build the binary **once**
   (`TestMain`/`sync.Once`), not per test.
3. **Phase-gated driving** (poll a phase classifier to act at the right instant) rather
   than fixed sleeps. The phase helpers are a prototype of the future reconciled state
   query. A phase-specific test **refuses to run (SCAFFOLD-FAIL, never false-green) if its
   phase was never observed.**
4. **Four assertion outcomes** — live-pass, baseline, pending, scaffold-fail — made
   **machine-distinguishable** at the reporting boundary (below), since `go test` itself
   only has PASS/FAIL/SKIP.

## Architecture

```
packages/ccpool/cmd/ccpool/
  contract_test.go          //go:build contract — scenarios (TestContract_*)
  contract_harness_test.go  //go:build contract — helpers (sandbox, phase gates, outcomes)
  testdata/fake-claude      (existing; unchanged)
```

### Harness helpers (`contract_harness_test.go`)

- `setupSandbox(t)` — per-test `t.TempDir()` XDG dirs + a **unique tmux socket derived
  from the tempdir** + rendered `config.toml` with the pinned `plugin_dir` of the binary
  under test and **`[notify] adapter = "none"`** (a table, not a top-level key —
  `internal/config/config.go:28-31`). `t.Cleanup` kills the tmux server + removes the dir.
- Binary under test built **once** in `TestMain`; `CCPOOL_BIN` env override → the
  **installed** binary. _(The real plugin's hook command is an absolute path
  (`default.nix`), and the `/tmp/cc-t9` prototype confirmed `new`→`ready` fires under a
  sandboxed XDG — so the SessionStart handshake works in the sandbox.)_
- `ccp(t, …)`→`(stdout, stderr, exit)`; `cap(name)`→pane text.
- Phase gates `waitForThinking/Streaming/Idle/NeedsInput(t)` — poll with a per-phase
  budget; on timeout call `scaffoldFail` (`pane rendering may have changed`), **not** a
  command-regression failure.
- Outcome helpers — `liveAssert`, `baseline`, `pending`, `scaffoldFail` — each emits a
  **structured `OUTCOME=…` line** (test, outcome, bead, observed/want) so a post-processor
  (or `go test -json`) classifies a run into the four buckets. **`pending()` must be the
  last call in a test** (after every objective assert) so it never short-circuits a live
  assert.

### TTY-dependent `attend` branches

v1 is additive, so no `attend.go` refactor and no PTY dependency. Test what's reachable
without a TTY: the **no-TTY list**, `--include-done`, **zero-candidate**, and the pure
`attendCandidates` filtering (already unit-testable, `attend_test.go`). The
**numbered/fzf branch selection** (gated on `stdinIsTerminal()`/`LookPath("fzf")`,
`attend.go:77-89`) is a **pending** note → needs an injection refactor, scheduled with the
bug-fix phase.

### Model / cost / time budget

- Launch model via config `default_model`; pin a **named fallback model** known to expose
  high reasoning effort (not just "the cheapest") — the TUI contract is model-independent
  but the _thinking phase you must reproduce_ is effort-dependent.
- **Thinking scenarios pin an explicit high-reasoning-effort prompt** and gate the body on
  `waitForThinking` (else SCAFFOLD-FAIL). Trivial prompts; cancel mid-turn so output stays
  tiny.
- Rough budget: ~8–12 real-claude turns × ~30–40s (launch + thinking + act) + one build ≈
  **~8–12 min** wall-clock. Hence `-timeout=0` and a documented per-test budget.

### Provisioning

- `nix run .#ccpool-contract` → `go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/...`
  piped through a small classifier that prints the four-bucket summary. Setup guard skips
  the suite with a clear message if `claude` is absent/unauthenticated.
- Never in `nix flake check` / pre-commit. `doCheck` stays at its `buildGoModule` default;
  the build tag keeps `contract_test.go` out of the default check phase — **verify** this
  (see exit criteria).

## The four assertion outcomes

| Outcome           | Go mechanism                                                                                                   | Fails suite?      | Use                                                                                                                                                                                                                       |
| ----------------- | -------------------------------------------------------------------------------------------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Live pass**     | normal assert + `OUTCOME=live`                                                                                 | yes               | objective facts: exit codes, timeouts, tmux liveness, deterministic stdout/stderr, and `doctor`-parseable `live=`/`cwd_trusted=`                                                                                          |
| **Baseline**      | `baseline(t,bead,desc,got,wantObserved)` — asserts the **currently observed** value; `OUTCOME=baseline bead=…` | **yes, on drift** | known-but-deferred desired value (e.g. thinking-cancel exit 6). Pinning observed makes any change — incl. a future fix — fail loudly and locate itself; the baseline calls in code **are** the expected-deferred manifest |
| **Pending**       | `pending(t,desc,obsNeeded)` → structured log + `t.Skip` **last**                                               | no                | nothing checkable yet; needs observability                                                                                                                                                                                |
| **Scaffold-fail** | `scaffoldFail(t,…)` on phase-gate timeout / phase-never-observed; `OUTCOME=scaffold`                           | yes               | the harness's own driving broke (likely pane-rendering drift) — explicitly _not_ a verdict on the command                                                                                                                 |

The `OUTCOME=` lines (or `-json` + classifier) keep baseline-drift, scaffold-fail, and
genuine regressions **distinguishable** at the reporting boundary — otherwise `go test`
renders all three as identical FAILs. The suite exits nonzero on any live failure,
baseline drift, or scaffold-fail; pending prints/harvests but never gates.

## Test catalog (v1)

| Area                 | Scenario                                                                   | Live / baseline asserts                                                                                                                                                                 | Pending (→ obs)                                                                                                                                                                          |
| -------------------- | -------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **lifecycle**        | `new`→ready; resume cold; `close`; `close --purge`                         | exit 0 in budget; tmux live/gone; purged row absent; `doctor` `live=`/`cwd_trusted=`                                                                                                    | state _truly_ ready (doctor `state=` is **cached**); resumed same conversation                                                                                                           |
| **send**             | reply short turn; `--no-wait` timing; busy refusal; leading-`/` guard      | exit 0/in-budget; **busy → exit 5** (baseline); `--no-wait` returns fast                                                                                                                | reply text; `working` state; message-not-command                                                                                                                                         |
| **cancel**           | streaming; thinking; idle; nonexistent; double; **stale-marker**           | streaming → 0; **thinking → 6 (baseline → cancel bug)**; idle → 0; nonexistent → nonzero; **stale-marker+thinking-cancel → wrong 0 (baseline; expected to FLIP when the bug is fixed)** | turn-stopped; reconciled idle; marker presence                                                                                                                                           |
| **interrupt**        | `--interrupt` streaming/thinking; `--queue-message`                        | streaming → 0/in-budget; **thinking → 1 (baseline)**                                                                                                                                    | should interrupt+deliver; no rewind/re-inject; _interrupt collapses `ErrCancelUnconfirmed` into generic exit 1 — should get a distinct code (see `exit-code-1-is-general-error` memory)_ |
| **attend**           | no-TTY list; `--include-done`; zero; candidate filtering                   | deterministic output + exit 0; filtering via unit tests                                                                                                                                 | numbered/fzf **branch selection** (needs injection refactor)                                                                                                                             |
| **needs_input**      | AskUserQuestion (no Notification → transcript fallback, `send.go:113-126`) | reaches `needs_input` within budget (baseline)                                                                                                                                          | the question text / associated info                                                                                                                                                      |
| **reap/concurrency** | 2+ sessions; **`reap` eviction**                                           | objective: `reap` evicts oldest-by-`last_activity` down to cap (`reap.go:40`) — note `new` does **not** enforce the cap                                                                 | reaped-state correctness                                                                                                                                                                 |

`attend` fixtures need **both** a store row **and** a live tmux pane — the filter drops
paneless rows (`attend.go:28`). `doctor` already prints `state= live= cwd_trusted=`
(`doctor.go:40`), but its `state=` is the **cached store row** — so "state truly ready"
stays pending; only `live=`/`cwd_trusted=` are live-assertable now.

## Observability requirements harvest (the v2 spec, seeded by v1)

| Pattern                                                | Observability it demands                                                                |
| ------------------------------------------------------ | --------------------------------------------------------------------------------------- |
| "reached idle / still working after X"                 | reconciled state query (transcript-first, pane fallback) — beyond `doctor`'s cached row |
| "old turn stopped before new"; "no rewind / re-inject" | event-log JSONL — ordered transitions + input actions w/ timestamps                     |
| "interrupt collapses into exit 1"                      | distinct exit code on the interrupt path (per `exit-code-1-is-general-error`)           |
| "reply text / which question is pending"               | state `--json` associated info                                                          |
| "marker still present"                                 | golden-marker canary (the one legitimate pane assertion), post-v2                       |

Next phase reroutes `cancel`/`reply --interrupt` confirmation through the reconciled
classifier (extending `doctor`) instead of `interruptLanded`'s marker grep — fixing the
thinking-phase cancel bug.

## Phase exit criteria

1. Harness mechanics work (isolation, phase-gated driving with SCAFFOLD-FAIL, disabled
   suite timeout + per-test budgets, build-once, exit capture, `doctor` parsing,
   `OUTCOME=` classification).
2. **`nix build .#ccpool` succeeds with the contract files present** (proves the tag
   excludes them from the default checkPhase; `doCheck` unchanged).
3. The catalog is written; every red is triaged: fixed (harness bug), `pending`, or
   `baseline` (observed value pinned + linked bead).

100% green is **not** required; a few reviewed baselines pinning known bugs are expected.

## Operational constraints / risks

- **Auth is not isolated.** XDG sandboxing covers config/store/socket, but `truster`
  writes folder-trust to the real `~/.claude.json` (`cancel.go:71`, `new.go:61`) and OAuth
  is shared → run serially; avoid concurrent interactive Claude use during a run.
- **Cost/time** ~8–12 min, ~8–12 turns; keep prompts trivial, cancel mid-turn.
- **Flakiness** — real-model latency vs phase-poll budgets; SCAFFOLD-FAIL makes gate
  timeouts self-identify rather than masquerade as command regressions.

## Follow-up work (beads to create on approval)

- **feature**: ccpool contract test harness (this spec).
- **bug**: cancel / `reply --interrupt` unreliable during thinking (`interruptLanded` only
  matches the streaming marker; double-Escape triggers rewind; false-positives on stale
  markers; interrupt collapses the unconfirmed error into exit 1). Fix via a reconciled
  state classifier. Evidence: `pg2-oapb` notes + `/tmp/cc-t9`.
- **feature (v2)**: reconciled state query (extend `doctor`) + JSONL event log + `attend`
  injection refactor, scoped by the harvested pending/baseline gaps.

## Evidence trail

- Verification findings: bead `pg2-oapb` notes (2026-06-11).
- Prototype + captured panes: `/tmp/cc-t9/` — basis for the Go phase helpers; confirmed
  `new`→`ready` under sandbox.
- Design reviews: two subagent critiques (2026-06-11) — drove the baseline/expected-
  deferred model, SCAFFOLD-FAIL, the Go runner, the `-timeout=0`/serial/build-once
  mechanics, machine-distinguishable outcomes, dropping the PTY dep, `doctor` re-triage,
  the `reap`-not-`new` cap correction, and the auth-isolation note.
