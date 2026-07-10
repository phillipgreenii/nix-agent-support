# Runbook (prompt): refresh pa-monitor's model tables

**This file is a runnable prompt.** Execute it with:

```bash
claude -p "$(cat packages/pa-monitor/docs/runbooks/refresh-model-tables.md)"
```

or paste it into an interactive session. It is self-contained — it encodes the
sources of truth, the domain gotchas, and the exact procedure so the task is
mechanical rather than re-derived each time.

---

## Goal

Verify — and update if drifted — the three hardcoded, drift-prone tables that
pa-monitor keeps about Claude models. All three go stale when Anthropic ships a
new model or changes pricing/limits, and each has a **different** source of
truth.

| #   | Table            | File                                                             | Governs                                                                             |
| --- | ---------------- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| 1   | Context windows  | `internal/core/models/windows.go`                                | model → context-window tokens (drives the TUI `Context: X / Y (Z%)` line)           |
| 2   | Per-token prices | `internal/core/usage/pricing.go` (`PriceTable`)                  | model → per-MTok USD (input/output/cache); drives native block/week cost            |
| 3   | Plan caps        | `internal/core/usage/plan_caps.go` (`PlanCapUSD` / `WeekCapUSD`) | plan tier → 5h/week soft-cap USD (a **conservative estimate** of hour-based limits) |

## When to run

- **Periodically** (e.g. monthly), or
- **On a new Claude model launch**, or
- **When you notice a problem** — e.g. context % > 100% or absurdly low, wrong
  cost figures, or cap warnings firing at the wrong time.

## Sources of truth — you MUST NOT answer from memory

- **Windows (table 1):** the Anthropic **Models API** `max_input_tokens` field.
  This is authoritative and programmatic:

  ```bash
  ant models list --transform '{id, max_input_tokens}' --format jsonl
  ```

  (First-party API only. If `max_input_tokens` is absent, the CLI/SDK is old —
  fall back to `client.models.retrieve("<id>").max_input_tokens`.)

- **Prices (table 2):** Anthropic's published per-token pricing (per-MTok
  input, output, cache-write, cache-read). WebFetch
  `https://platform.claude.com/docs/en/pricing.md`, or read the `claude-api`
  skill's current-models table. There is no `ant` field for price — use the docs.
- **Plan caps (table 3):** Anthropic's published plan limits (Pro / Max 5x /
  Max 20x), which are **hours-based**. There is no clean $ number — keep the
  existing conservative upper-bound $ estimates and only nudge them when the
  announced limits change materially. This table is inherently fuzzy; do not
  chase precision.

The `claude-api` skill's model catalog is a convenient cross-check for windows
and prices, but note it is a **cached snapshot** — prefer the live Models API /
pricing docs, and say so if you had to fall back to the cache.

## Domain gotchas — already learned, do not re-derive

- **Transcripts store the BARE model id** (`claude-opus-4-8`, `claude-sonnet-5`),
  NOT a `[1m]` suffix — the suffix is a harness display artifact. `windows.go`
  keys on bare ids. The `[<n>k|m]` suffix path exists only for the explicit-beta
  case and as a safety net.
- **Current frontier models ship 1M as standard** (Opus 4.6/4.7/4.8, Sonnet
  5/4.6, Fable 5). **Haiku-tier is 200k.** The **legacy 4.5 family defaults to
  200k** (1M only via the `[1m]` beta) — keep those rows frozen at 200k; do not
  copy the Models API max onto them.
- **`windows.go` has a family-prefix fallback** (`opus`/`sonnet`/`fable`/`mythos`
  → 1M, `haiku` → 200k, reported `known=false`). Because of this, a brand-new
  frontier model **auto-resolves to 1M without any edit**. So you only _need_ to
  touch `windows.go` when (a) a new model **breaks the family pattern** (a new
  small-context model, or a Haiku that goes 1M), or (b) you want an
  **authoritative** `known=true` entry instead of the heuristic guess.
- `max_input_tokens` is the model's **maximum** window. For current models that
  equals the bare-id default (1M). For beta-gated legacy models it does not —
  see the 4.5 note above.
- For **prices**, a genuinely new model needs a `PriceTable.Models` entry; the
  `Default` catches unknowns but at possibly-wrong rates. Cost on the current
  plan is notional (ADR-0021), so this is lower-stakes than windows.

## Procedure

1. **(optional) Track it:** `bd create "pa-monitor: refresh model tables (<date>)" -t task -p 3`,
   claim it. Skip for a quick check.
2. **Pull the live values** from the three sources above. Record them.
3. **Diff** each live value against the committed table. Produce a short drift
   list: `model/tier → old → new` for each of windows / prices / caps. If there
   is **no drift, stop here** and report "no drift" — do not make edits.
4. **Update only the drifted rows**, honoring the gotchas (bare ids; 4.5 frozen;
   family fallback means new-frontier-model windows may need no edit; conservative
   caps).
5. **TDD the changes:** update the corresponding test expectation FIRST
   (`windows_test.go` for table 1; the pricing / caps tests for 2 and 3), watch
   it fail, then edit the table so it passes. Never edit a table without its test
   moving in lockstep.
6. **Validate** (all MUST pass):
   ```bash
   cd packages/pa-monitor && gofmt -l internal/ | grep -v pb.go   # empty = clean
   go vet ./... && go test ./...
   cd ../.. && nix build .#pa-monitor --no-link
   ```
7. **Report**: the drift found, the edits made (or "no drift"), and the source
   each value came from (live API vs cached catalog). Then commit.

## Committing (this repo's convention)

- Verify the branch first (`git branch --show-current`). This repo commits to
  **local `main`, unpushed**; a plain rebuild reverts unpushed work unless it is
  pushed + relocked via `pn workspace update --siblings-only`. Push is a separate,
  later step — do not push unless asked.
- Reference the bead in the body (`Bead: <id>`). The `Refs:` line is Jira-only;
  beads ids (`pg2-*`) do not use it.
- Do not stage unrelated dirty files (e.g. in-progress `docs/behavior/*`).

## Auth note

`ant` and the Models API need first-party credentials. If
`ant auth status` reports no active credential source, you cannot hit the live
Models API — fall back to the `claude-api` skill's cached catalog + a WebFetch of
the models/pricing docs, and **state in your report that the values are cached,
not live**. Do not invent values.

## Known nit while you're here

`plan_caps.go`'s doc comment still says the caps are "ccusage-published" — that
provenance is stale (pa-monitor no longer uses ccusage; see the pg2-5ddg7
investigation). Reword it to "Anthropic-published plan limits" if you touch that
file.
