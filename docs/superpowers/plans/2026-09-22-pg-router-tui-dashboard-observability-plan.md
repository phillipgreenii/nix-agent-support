# pg-router TUI observability (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix pg-router's TUI so an operator can tell, at a glance, which sources/listeners exist, whether they're actually healthy (not just "stale" from a wrong threshold), what's happening right now, and that `pr.reconcile`'s queue depth is a heartbeat count, not a to-do list — with no reflowing layout.

**Architecture:** Widen the existing wire reply (`internal/tui/reply.go`, fed by `internal/core/core.go`'s `composeStatusReply`) with a handful of additive fields, each backed by data that mostly already exists inside pg-router (a per-source trigger interval already resolved by config, a decline-reason breakdown already landed on the wire but never decoded, self-report state already computed for handlers). Then update `internal/tui/panes.go`/`model.go` to render the new fields and reorder the zone list into two tiers.

**Tech Stack:** Go 1.x, `charmbracelet/lipgloss` for TUI rendering, table-driven tests (`go test`), this repo's existing `nix build .#checks.<system>.pg-router-go-tests` gate.

**Spec:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/docs/superpowers/specs/2026-09-22-pg-router-tui-dashboard-observability-design.md`

## Global Constraints

- Every wire change MUST be additive: no renamed or removed JSON keys on `StatusReply`/`Source`/`Listener`/`Registration`. (spec: "Data model changes")
- Every new field added to `Source`/`Listener` MUST also be declared in `schemas/cli.status-reply.schema.json`'s corresponding item `properties` in the SAME task. That schema sets `additionalProperties: false`, and the running TUI validates every real poll reply against it (`internal/tui/poller.go`'s `core.DiscriminateReply`) — an undeclared field breaks the TUI in production, not just a fixture. (found in plan review, 2026-09-22)
- `Source.ExpectedIntervalMs == 0` MUST render `N/A`, never `stale`. (spec: "Source" staleness rule)
- A source that has never ticked (`LastTick.IsZero()`) MUST render `idle`, taking precedence over both `N/A` and `stale`, exactly as it does today. (spec: "Sources pane" mockup note)
- `Listener.Declined` stays the existing flat per-listener total; any reason breakdown derived from `DeclinedByReason` MUST sum to it, including when a `DeclineDetail` override string is present. (spec: "Listener" population notes)
- The standalone Registry pane is removed; self-report state folds into the Listeners pane as a column, joined by `Registration.ID == Listener.Role`. (spec: "Registry pane removal")
- The static tier (Listeners, Sources) MUST NOT change position or size when the dynamic tier (Queues, Activity) changes. (spec: "Layout: two tiers")
- Activity renders at most the 8 most recent entries with relative (`Xs ago`/`Xm ago`) timestamps, never absolute `HH:MM:SS`. (spec: "Activity pane")
- A queue type in the heartbeat set (starts with `pr.reconcile`) renders without the depth bar and with a `(heartbeat)` label; no new metric or PR-content classification is added. (spec: "pr.reconcile relabeling", "Non-goals")
- No changes to `pg-connector` or to `phillipg-nix-ziprecruiter`'s query configuration. (spec: "Scope", "Non-goals")

## Review Focus

- A source with a configured `expected_interval` that has never ticked MUST still render `idle`, not `N/A` — precedence, not just presence of the new field, decides the display. Task 1's tests cover this explicitly.
- The Activity ring can legitimately hold fewer than 8 entries (right after startup) — capping/slicing MUST NOT panic or pad with fake rows. Task 6's tests cover this.
- Under extreme height pressure, the dynamic tier (Activity, then Queues) MUST be fully gone before the static tier (Listeners, Sources) ever drops one row. Task 4's tests cover this.
- A `DeclineDetail` override string that happens to equal the literal text `"busy"` or `"unavailable"` is indistinguishable, at the point this code reads it, from the canonical `DeclineReason` string — this is a pre-existing ambiguity in the data this design surfaces, not something Task 2 can resolve. Task 2's test proves the three-way sum invariant still holds in that collision case, rather than asserting a classification this code cannot make.
- A role declared in config but never dispatched to (zero delivered/declined/handler-failures, never self-reported) MUST render clean zero/empty states (`0`, `-`, `—`) — never a blank cell or a crash from a missing map entry. Covered across Task 2 and Task 3's tests.

---

## Task 1: Per-source staleness uses the source's own interval

**Files:**

- Modify: `internal/config/registry.go` — new optional `expected_interval` TOML field + interval resolution
- Modify: `internal/core/core.go` — `Options.SourceIntervalsMs`, `Service.sourceIntervalsMs`, `statusSources` widening
- Modify: `cmd/pg-router/run.go` — compute and wire `SourceIntervalsMs` into `core.Options`
- Modify: `internal/tui/reply.go` — `Source.ExpectedIntervalMs`
- Modify: `internal/tui/panes.go` — `sourceHealthText`, `renderSourcesPane`, new `sourceNextCheckText`/`formatCoarse` helpers
- Modify: `internal/tui/model.go` — drop the now-unused `tickIntervalMs` argument at the Sources pane call site
- Modify: `schemas/cli.status-reply.schema.json` — declare `sources[].expectedIntervalMs` (REQUIRED for this task to be safe to ship — see Step 9a: `additionalProperties: false` means the running TUI's own schema validation, `internal/tui/poller.go`'s `core.DiscriminateReply`, will reject every poll reply once `composeStatusReply` starts emitting this field, unless the schema declares it first)
- Fix (pre-existing, breaks under this task's signature change): `internal/tui/panes_optionalfields_test.go:46,64` — both call `renderSourcesPane` with the old 7-arg (`tickIntervalMs`-included) signature
- Fix (pre-existing, semantic regression under this task's new precedence): `internal/tui/panes_test.go`'s `TestPanes_DerivedHealthTwoAxes` source subtest (`panes_test.go:60-81`) — calls the old 4-arg `sourceHealthText`, and two of its cases construct a `Source` with `ExpectedIntervalMs` left at its zero value, which this task's new precedence renders `N/A` instead of the asserted `stale`/`ok`
- Test: `internal/config/registry_test.go`, `internal/core/status_test.go`, `internal/tui/panes_test.go`

**Interfaces:**

- Consumes: `internal/query.PeriodTrigger{Every time.Duration}`, `internal/query.Query.Trigger() Trigger` (both already exist, verified in `internal/query/trigger.go:27` and `internal/query/query.go:74`).
- Produces: `Source.ExpectedIntervalMs int64` (wire field, ms, 0 = unknown), consumed by Task 1's own rendering and by no other task.

- [ ] **Step 1: Write the failing config test**

Verified against this file's existing tests (`internal/config/registry_test.go:160-190`): the decode entry point is package-level `Load() (Config, error)` (`internal/config/config.go:358`), driven by `writeCfg(t, body string)` + `absentGlobalConfig(t)` (both in `internal/config/config_test.go`, same package, already used by every existing `registry_test.go` test).

```go
// internal/config/registry_test.go
func TestExpectedIntervalMsFor_ExplicitOverrideWins(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[role]]
name = "worker"
binds = ["x"]

[[query]]
name = "push-ish"
emits = ["x"]
type = "command"
expected_interval = "45s"
[query.command]
argv = ["true"]
format = "jsonl"

[query.trigger]
every = "10s"
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := ExpectedIntervalMsFor(c.Queries[0], c.ExpectedIntervalOverrides)
	if want := int64(45_000); got != want {
		t.Fatalf("ExpectedIntervalMsFor() = %d, want %d (override must beat the trigger's own 10s)", got, want)
	}
}

func TestExpectedIntervalMsFor_PeriodTriggerFallsBackToResolvedEvery(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[role]]
name = "worker"
binds = ["x"]

[[query]]
name = "period-source"
emits = ["x"]
type = "command"
[query.command]
argv = ["true"]
format = "jsonl"

[query.trigger]
every = "90s"
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := ExpectedIntervalMsFor(c.Queries[0], c.ExpectedIntervalOverrides)
	if want := int64(90_000); got != want {
		t.Fatalf("ExpectedIntervalMsFor() = %d, want %d", got, want)
	}
}

func TestExpectedIntervalMsFor_ThresholdTriggerIsUnknown(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[role]]
name = "worker"
binds = ["x"]

[[query]]
name = "threshold-source"
emits = ["x"]
type = "command"
[query.command]
argv = ["true"]
format = "jsonl"

[query.trigger]
kind = "threshold"
count = 5
binds = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := ExpectedIntervalMsFor(c.Queries[0], c.ExpectedIntervalOverrides)
	if got != 0 {
		t.Fatalf("ExpectedIntervalMsFor() = %d, want 0 (unknown)", got)
	}
}
```

The `[query.command]` table above already includes both required sub-fields (`argv`, `format` — `query.CommandQuery.Validate()`, `internal/query/command.go:29`, hard-requires `format` to be `jsonl`/`json`), copied from the real passing fixture at `registry_test.go:163-180`. If this file's `command`-type fixture shape has changed since, re-copy from whatever fixture in this file currently passes rather than trusting the snippet above verbatim.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run ExpectedInterval -v`
Expected: FAIL — `ExpectedIntervalMsFor` and the `expected_interval` TOML field do not exist yet.

- [ ] **Step 3: Implement the config field and resolver**

In `internal/config/registry.go`, add the optional field to `queryTOML` (`registry.go:123-134`), reusing the existing `duration` wrapper type (`registry.go:18`, already used by `triggerTOML.Every *duration` at `:140`):

```go
type queryTOML struct {
	Name    string    `toml:"name"`
	// ... existing fields unchanged ...
	// ExpectedInterval is an optional, explicit override for staleness
	// display purposes (this task): unlike Trigger.Every, it carries no
	// scheduling meaning — it exists so a future non-period-triggered or
	// push-mode source can still declare its own expected cadence. An
	// explicit value always wins over a period trigger's own resolved
	// Every.
	ExpectedInterval *duration `toml:"expected_interval"`
}
```

Add `Config.ExpectedIntervalOverrides map[string]int64` (`internal/config/config.go`'s `Config` struct — `Config` opens at `config.go:32`; its `Queries query.SourceSet` field is at `config.go:161` — add the new field alongside `Queries`, confirming the exact current line before editing since this file may have grown).

Widen `buildQueries` (`registry.go:268-290`) to also return the override map, and its one call site (`registry.go:257-262`) to capture it:

```go
func (r *Registry) buildQueries(md toml.MetaData, qts []queryTOML, c Config) (query.SourceSet, map[string]int64, []error) {
	var out query.SourceSet
	overrides := map[string]int64{}
	var errs []error
	seen := map[string]bool{}
	for i, qt := range qts {
		if qt.Name == "" {
			errs = append(errs, fmt.Errorf("query[%d]: name is required", i))
			continue
		}
		if seen[qt.Name] {
			errs = append(errs, fmt.Errorf("duplicate query name %q", qt.Name))
			continue
		}
		seen[qt.Name] = true
		q, err := r.buildQuery(md, qt, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("query[%d] %q: %w", i, qt.Name, err))
			continue
		}
		out = append(out, query.Source{Name: qt.Name, Query: q})
		if qt.ExpectedInterval != nil {
			overrides[qt.Name] = qt.ExpectedInterval.D.Milliseconds()
		}
	}
	return out, overrides, errs
}
```

```go
	queries, overrides, qerrs := r.buildQueries(md, shape.Queries, *c)
	errs = append(errs, qerrs...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	c.Queries = queries
	c.ExpectedIntervalOverrides = overrides
	return out, nil
```

Add the resolver, exported so `cmd/pg-router/run.go` (Task 1, Step 5 below) can call it, taking one `query.Source` rather than scanning the whole set per call:

```go
// ExpectedIntervalMsFor returns, in milliseconds, src's expected tick
// cadence: an explicit `expected_interval` config override (overrides,
// keyed by src.Name) always wins; otherwise a `kind: "period"` query's own
// resolved trigger interval; otherwise 0 (unknown — threshold/manual
// triggers, or a period query with neither).
func ExpectedIntervalMsFor(src query.Source, overrides map[string]int64) int64 {
	if ms, ok := overrides[src.Name]; ok {
		return ms
	}
	if pt, ok := src.Query.Trigger().(query.PeriodTrigger); ok && pt.Every > 0 {
		return pt.Every.Milliseconds()
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -run ExpectedInterval -v`
Expected: PASS

- [ ] **Step 5: Wire `SourceIntervalsMs` through `core.Options` and `cmd/pg-router/run.go`**

In `internal/core/core.go`, add to `Options` (near `ExcludedSources`, `core.go:180-181`):

```go
	// SourceIntervalsMs is source name -> expected tick cadence in
	// milliseconds (this task): resolved once by the caller
	// (cmd/pg-router's bootCore, via config.ExpectedIntervalMsFor) from the
	// FULL configured query set, so statusSources can report an
	// interval-aware staleness state instead of the pool-wide tick. A
	// missing or zero entry means "unknown" — statusSources renders that
	// source's ExpectedIntervalMs as 0, and the TUI renders N/A.
	SourceIntervalsMs map[string]int64
```

Add to `Service` (near `excludedSources`, `core.go:281`):

```go
	sourceIntervalsMs map[string]int64
```

Find the assignment `s.excludedSources = opts.ExcludedSources` (or equivalent, wherever `Listen`/the `Service` constructor copies `Options` fields onto the new `Service` — grep `core.go` for `excludedSources:` or `excludedSources =`) and add the parallel line:

```go
	sourceIntervalsMs: opts.SourceIntervalsMs,
```

Widen `statusSources` (`core.go:1502`) to take and use the map:

```go
func statusSources(active []SourceReport, excludedSources []string, intervalsMs map[string]int64) []map[string]any {
	out := make([]map[string]any, 0, len(active)+len(excludedSources))
	for _, sr := range active {
		row := map[string]any{
			"name":               sr.Name,
			"type":               "pull",
			"enabled":            true,
			"excluded":           false,
			"mode":               "pull",
			"failure":            nil,
			"expectedIntervalMs": intervalsMs[sr.Name],
		}
		if !sr.LastTick.IsZero() {
			row["lastTick"] = sr.LastTick.UTC().Format(time.RFC3339Nano)
		}
		if sr.Failure != nil {
			row["failure"] = map[string]any{
				"count":        sr.Failure.Count,
				"nextEligible": sr.Failure.NextEligible.UTC().Format(time.RFC3339Nano),
			}
		}
		out = append(out, row)
	}
	for _, name := range excludedSources {
		out = append(out, map[string]any{
			"name": name, "type": "pull", "enabled": true, "excluded": true,
			"mode": "pull", "failure": nil, "expectedIntervalMs": intervalsMs[name],
		})
	}
	return out
}
```

Update both call sites in `composeStatusReply` (`core.go:1313` and `:1339`):

```go
"sources": statusSources(nil, s.excludedSources, s.sourceIntervalsMs),
```

```go
reply["sources"] = statusSources(tick.Sources, s.excludedSources, s.sourceIntervalsMs)
```

In `cmd/pg-router/run.go`, before the `opts := core.Options{...}` literal (`run.go:297`), compute the map from `cfg.Queries` (verified type `query.SourceSet`, e.g. `internal/config/config_test.go:1398-1403`) and `cfg.ExpectedIntervalOverrides` (Step 3):

```go
	sourceIntervalsMs := make(map[string]int64, len(cfg.Queries))
	for _, src := range cfg.Queries {
		sourceIntervalsMs[src.Name] = config.ExpectedIntervalMsFor(src, cfg.ExpectedIntervalOverrides)
	}
```

Add `SourceIntervalsMs: sourceIntervalsMs,` to the `opts := core.Options{...}` literal, alongside `ExcludedSources: excluded.Sources` (`run.go:313`).

- [ ] **Step 6: Write the failing TUI tests**

```go
// internal/tui/panes_test.go
func TestSourceHealthText_UnknownIntervalRendersNA(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Now().Add(-10 * time.Minute), ExpectedIntervalMs: 0}
	got := stripANSI(sourceHealthText(s, time.Now(), render.Theme{}))
	if got != "N/A" {
		t.Fatalf("sourceHealthText() = %q, want %q", got, "N/A")
	}
}

func TestSourceHealthText_NeverTickedIsIdleEvenWithKnownInterval(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Time{}, ExpectedIntervalMs: 30_000}
	got := stripANSI(sourceHealthText(s, time.Now(), render.Theme{}))
	if got != "idle" {
		t.Fatalf("sourceHealthText() = %q, want %q (idle must outrank N/A)", got, "idle")
	}
}

func TestSourceHealthText_UsesOwnIntervalNotCoreTick(t *testing.T) {
	// A source ticking every 5 minutes must read ok 2 minutes after its own
	// last tick, even though a fast core tick would flag it stale under the
	// OLD (pool-wide) threshold.
	s := Source{Enabled: true, LastTick: time.Now().Add(-2 * time.Minute), ExpectedIntervalMs: (5 * time.Minute).Milliseconds()}
	got := stripANSI(sourceHealthText(s, time.Now(), render.Theme{}))
	if got != "ok" {
		t.Fatalf("sourceHealthText() = %q, want %q", got, "ok")
	}
}

func TestSourceHealthText_StaleUsesOwnInterval(t *testing.T) {
	s := Source{Enabled: true, LastTick: time.Now().Add(-20 * time.Minute), ExpectedIntervalMs: (5 * time.Minute).Milliseconds()}
	got := stripANSI(sourceHealthText(s, time.Now(), render.Theme{}))
	if !strings.HasPrefix(got, "stale") {
		t.Fatalf("sourceHealthText() = %q, want prefix %q", got, "stale")
	}
}
```

NOTE: `stripANSI` may not exist yet in this file — if `panes_test.go`'s existing tests already compare rendered output, grep for how they strip lipgloss styling (likely an existing helper or a zero-value `render.Theme{}` that renders unstyled) and reuse that convention instead of inventing `stripANSI`.

- [ ] **Step 7: Run test to verify it fails**

Run: `go test ./internal/tui/... -run TestSourceHealthText -v`
Expected: FAIL — `sourceHealthText`'s current signature takes `tickIntervalMs int64`, not a `Source` with `ExpectedIntervalMs`, and `Source` has no such field yet.

- [ ] **Step 8: Implement**

In `internal/tui/reply.go`, add the field to `Source` (`reply.go:155-163`):

```go
type Source struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Enabled  bool      `json:"enabled"`
	Excluded bool      `json:"excluded"`
	Mode     string    `json:"mode"`
	LastTick time.Time `json:"lastTick"`
	Failure  *Failure  `json:"failure"`
	// ExpectedIntervalMs is this source's own expected tick cadence in
	// milliseconds (this task). 0 means unknown.
	ExpectedIntervalMs int64 `json:"expectedIntervalMs"`
}
```

In `internal/tui/panes.go`, replace `sourceHealthText` (`panes.go:96-115`):

```go
// sourceHealthText ranks a source's health: disabled > excluded > failing >
// idle > unknown-interval (N/A) > stale > ok. A source that has never
// ticked renders idle regardless of whether its interval is known -- idle
// outranks N/A, since "never started" and "cadence unknown" are different
// facts. Widened (this task) to use the source's OWN ExpectedIntervalMs
// instead of the pool-wide tickIntervalMs.
func sourceHealthText(s Source, now time.Time, theme render.Theme) string {
	switch {
	case !s.Enabled:
		return theme.Disabled.Render("disabled")
	case s.Excluded:
		return theme.Excluded.Render("excluded")
	case s.Failure != nil && s.Failure.Count > 0:
		return theme.Failing.Render(fmt.Sprintf("failing ×%d", s.Failure.Count))
	case s.LastTick.IsZero():
		return theme.Muted.Render("idle")
	case s.ExpectedIntervalMs <= 0:
		return theme.Muted.Render("N/A")
	case now.Sub(s.LastTick) > staleThreshold(s.ExpectedIntervalMs):
		return theme.Stale.Render("stale " + formatMinutes(now.Sub(s.LastTick)))
	default:
		return theme.OK.Render("ok")
	}
}

// formatCoarse renders d at second granularity below a minute, minute
// granularity at or above -- the same coarse convention formatSeconds/
// formatMinutes already establish, applied to whichever is appropriate.
func formatCoarse(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return formatSeconds(d)
	}
	return formatMinutes(d)
}

// sourceNextCheckText renders the Sources pane's "NEXT CHECK IN" column: a
// countdown when healthy and the interval is known, "overdue by <duration>"
// when stale, or "-" for every other health state (disabled/excluded/
// failing/idle/unknown-interval), since sourceHealthText's own column
// already explains why there is nothing to count down.
func sourceNextCheckText(s Source, now time.Time) string {
	if !s.Enabled || s.Excluded || s.LastTick.IsZero() || s.ExpectedIntervalMs <= 0 {
		return "-"
	}
	if s.Failure != nil && s.Failure.Count > 0 {
		return "-"
	}
	elapsed := now.Sub(s.LastTick)
	expected := time.Duration(s.ExpectedIntervalMs) * time.Millisecond
	if elapsed > staleThreshold(s.ExpectedIntervalMs) {
		return "overdue by " + formatCoarse(elapsed)
	}
	remaining := expected - elapsed
	if remaining < 0 {
		remaining = 0
	}
	return "~" + formatCoarse(remaining)
}
```

Replace `renderSourcesPane` (`panes.go:268-284`):

```go
func renderSourcesPane(sources []Source, now time.Time, width int, theme render.Theme, emptyMsg, title string) string {
	headers := []string{"SOURCE", "STATUS", "SINCE LAST TICK", "NEXT CHECK IN"}
	widths := []int{12, 8, 16, 18}
	rows := make([][]string, 0, len(sources))
	for _, s := range sources {
		sinceLastTick := "-"
		if !s.LastTick.IsZero() {
			sinceLastTick = formatCoarse(now.Sub(s.LastTick))
		}
		rows = append(rows, []string{
			textsafe.Sanitize(s.Name),
			sourceHealthText(s, now, theme),
			sinceLastTick,
			sourceNextCheckText(s, now),
		})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg, width)
}
```

In `internal/tui/model.go`, update the Sources call site (`model.go:491`) to drop the now-removed `tickIntervalMs` argument:

```go
content := renderSourcesPane(m.reply.Sources, now, width, m.theme, emptyStateText(es, "(no sources configured)"), title)
```

- [ ] **Step 9: Fix the two pre-existing tests this task's signature changes break**

`internal/tui/panes_optionalfields_test.go:46,64` both call the OLD 7-arg `renderSourcesPane(sources, tickIntervalMs, now, width, theme, emptyMsg, title)`. Update both call sites to the new 6-arg form, dropping the `tickIntervalMs` (`1000`) argument:

```go
got := renderSourcesPane(sources, now, 0, theme, "(no sources configured)", "Sources")
```

```go
got2 := renderSourcesPane([]Source{{Name: "flaky", Enabled: true, LastTick: now, Failure: failing}}, now, 0, theme, "", "Sources")
```

`internal/tui/panes_test.go`'s `TestPanes_DerivedHealthTwoAxes` source subtest (`panes_test.go:60-81`) calls the OLD 4-arg `sourceHealthText(c.s, 1000, now, theme)` — update to the new 3-arg form (`sourceHealthText(c.s, now, theme)`). Additionally, its `"stale when ticked long ago"` and `"ok when ticked recently"` cases construct a `Source` with no `ExpectedIntervalMs` set — under this task's new precedence that renders `N/A`, not `stale`/`ok`. Give both cases an explicit `ExpectedIntervalMs` (e.g. `(time.Minute).Milliseconds()`) consistent with whatever `LastTick` offset each case already uses, so the case still exercises the health state its name claims.

- [ ] **Step 10: Update the JSON schema — REQUIRED before this is safe to ship**

`schemas/cli.status-reply.schema.json` sets `"additionalProperties": false` on both the top-level reply and the `sources[]` item object (lines 5, 158). `internal/tui/poller.go:172` validates every real poll reply against this exact schema (`core.DiscriminateReply(reply, core.StatusReplySchema, &out)`) before decoding it. Without this step, `composeStatusReply` emitting `expectedIntervalMs` makes every single poll fail schema validation — a production break, not a test nuisance. Add the new property (optional, not in `required`) to the `sources` item's `properties` (after `"mode"` at line 165):

```json
          "expectedIntervalMs": { "type": "integer", "minimum": 0 },
```

- [ ] **Step 11: Run test to verify it passes**

Run: `go test ./internal/tui/... ./internal/config/... ./internal/core/... -v`
Expected: PASS. Also run `go build ./...` to confirm every other call site of `sourceHealthText`/`renderSourcesPane`/`statusSources` was updated (the compiler will point at any missed one).

- [ ] **Step 12: Update the golden fixture (compat fixture stays untouched)**

`conformance/testdata/golden/cli.status-reply.json` represents the current full reply shape — add `"expectedIntervalMs": 0` to its `sources[]` entries so it stays representative. Do **NOT** touch `conformance/testdata/compat/cli.status-reply.json` — `TestBackwardCompat_LegacyGoldens` (`conformance/conformance_test.go:38`) exists specifically to prove the OLD, pre-widening 4-field legacy shape still validates against the widened schema; adding new fields to it would defeat the one thing it tests. Run `go test ./conformance/... -v` to confirm both fixtures still pass.

- [ ] **Step 13: Commit**

```bash
git add internal/config/registry.go internal/config/registry_test.go internal/config/config.go \
  internal/core/core.go \
  cmd/pg-router/run.go \
  internal/tui/reply.go internal/tui/panes.go internal/tui/model.go internal/tui/panes_test.go internal/tui/panes_optionalfields_test.go \
  schemas/cli.status-reply.schema.json \
  conformance/testdata/golden
git commit -m "feat(tui): use each source's own expected interval for staleness"
```

---

## Task 2: Listener recency, decline-reason breakdown, and self-report fold-in

**Files:**

- Modify: `internal/core/core.go` — `ListenerCounts.LastDeliveredAtNanos`, `statusListeners` widening (self-report join)
- Modify: `cmd/pg-router/run.go` — `listenerCountObserver.OnAccept` records the timestamp; clock seam
- Modify: `internal/tui/reply.go` — `Listener.LastDeliveredAtMs`, `Listener.DeclinedByReason`, `Listener.SelfReportState`, `Listener.DeclinedBucketed()`
- Modify: `internal/tui/panes.go` — `renderListenersPane` widened columns; drop `renderRegistryPane` call site; `model.go`'s Registry zone removed
- Modify: `internal/tui/model.go` — remove the Registry pane from the zone loop entirely
- Modify: `schemas/cli.status-reply.schema.json` — declare `listeners[].lastDeliveredAtMs` and `listeners[].selfReportState` (same reason as Task 1 Step 10 — `additionalProperties: false` on the `listeners[]` item object, `schemas/cli.status-reply.schema.json:105`)
- Test: `cmd/pg-router/run_test.go`, `internal/core/status_test.go`, `internal/tui/panes_test.go`

**Interfaces:**

- Consumes: `internal/core.Registration` (existing, `ID`/`Kind`/`State`/`Self`), `s.reg.List()` (existing, `internal/core/core.go:1276`).
- Produces: `Listener.DeclinedBucketed() (busy, unavailable, other int64)`, consumed by Task 2's own rendering only.

- [ ] **Step 1: Write the failing core test**

```go
// internal/core/status_test.go
func TestListenerCounts_LastDeliveredAtNanos_ZeroUntilSet(t *testing.T) {
	c := &ListenerCounts{}
	if got := c.LastDeliveredAtNanos.Load(); got != 0 {
		t.Fatalf("zero value LastDeliveredAtNanos = %d, want 0", got)
	}
}

func TestStatusListeners_SelfReportStateJoinsByRoleName(t *testing.T) {
	declared := []roles.Role{{Name: "df-feedback", Enabled: true, Binds: []string{"pr.changed"}}}
	counts := map[string]*ListenerCounts{"df-feedback": {}}
	// Registration.State is conformance.Lifecycle (an int-based Stringer,
	// internal/conformance's top-level conformance package,
	// conformance/transport.go:14), not a plain string -- conformance.Started
	// is the constant whose .String() is "started" (matching the existing
	// pattern at core.go:1411, `"state": r.State.String()`).
	regs := []Registration{{ID: "df-feedback", Kind: "handler", State: conformance.Started}}

	rows := statusListeners(declared, nil, counts, regs)
	if got := rows[0]["selfReportState"]; got != "started" {
		t.Fatalf("selfReportState = %v, want %q", got, "started")
	}
}

// (Import "github.com/phillipgreenii/pg-router/conformance" in this test
// file if it is not already imported.)

func TestStatusListeners_SelfReportStateEmptyWhenNeverRegistered(t *testing.T) {
	declared := []roles.Role{{Name: "df-feedback", Enabled: true, Binds: []string{"pr.changed"}}}
	counts := map[string]*ListenerCounts{"df-feedback": {}}

	rows := statusListeners(declared, nil, counts, nil)
	if got := rows[0]["selfReportState"]; got != "" {
		t.Fatalf("selfReportState = %v, want empty string", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/... -run "LastDeliveredAtNanos|SelfReportState" -v`
Expected: FAIL — `ListenerCounts` has no `LastDeliveredAtNanos` field, and `statusListeners` does not take a `regs` parameter yet.

- [ ] **Step 3: Implement in core.go**

Add to `ListenerCounts` (`core.go:199-215`):

```go
	// LastDeliveredAtNanos is UnixNano of the most recent successful
	// delivery (this task) -- 0 means never delivered. Set by
	// listenerCountObserver.OnAccept, the same call site Delivered already
	// increments at.
	LastDeliveredAtNanos atomic.Int64
```

Widen `statusListeners` (`core.go:1445`) with a `regs []Registration` parameter and the self-report join:

```go
func statusListeners(declared []roles.Role, excludedRoles []string, counts map[string]*ListenerCounts, regs []Registration) []map[string]any {
	excluded := make(map[string]bool, len(excludedRoles))
	for _, n := range excludedRoles {
		excluded[n] = true
	}
	selfState := make(map[string]string, len(regs))
	for _, r := range regs {
		// r.State is conformance.Lifecycle (int-based Stringer), not a
		// string -- .String() matches the existing pattern this file
		// already uses at core.go:1411.
		selfState[r.ID] = r.State.String()
	}
	out := make([]map[string]any, 0, len(declared))
	for _, r := range declared {
		binds := make([]string, len(r.Binds))
		copy(binds, r.Binds)
		var delivered, declined, lastDeliveredAtMs int64
		declinedByReason := map[string]int64{}
		if c := counts[r.Name]; c != nil {
			delivered = c.Delivered.Load()
			declined = c.Declined.Load()
			declinedByReason = c.DeclinedByReasonSnapshot()
			if nanos := c.LastDeliveredAtNanos.Load(); nanos != 0 {
				lastDeliveredAtMs = nanos / int64(time.Millisecond)
			}
		}
		out = append(out, map[string]any{
			"role":              r.Name,
			"binds":             binds,
			"enabled":           r.Enabled,
			"excluded":          excluded[r.Name],
			"delivered":         delivered,
			"declined":          declined,
			"declinedByReason":  declinedByReason,
			"lastDeliveredAtMs": lastDeliveredAtMs,
			"selfReportState":   selfState[r.Name],
			"backoff":           nil,
		})
	}
	return out
}
```

Update the call site (`core.go:1310`):

```go
"listeners": statusListeners(s.declaredRoles, s.excludedRoles, s.listenerCounts, regs),
```

(`regs` is already in scope at this point — it is the same `regs := s.reg.List()` at `core.go:1276` that `"registry": statusRegistrations(regs)` on the very next lines already uses. No new call to `s.reg` is needed.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/... -v`
Expected: PASS

- [ ] **Step 5: Write the failing observer test**

```go
// cmd/pg-router/run_test.go
func TestListenerCountObserver_OnAccept_RecordsLastDeliveredAt(t *testing.T) {
	counts := map[string]*core.ListenerCounts{"df-feedback": {}}
	fixedNow := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	obs := &listenerCountObserver{counts: counts, now: func() time.Time { return fixedNow }}

	obs.OnAccept("evt-1", "df-feedback")

	got := counts["df-feedback"].LastDeliveredAtNanos.Load()
	if want := fixedNow.UnixNano(); got != want {
		t.Fatalf("LastDeliveredAtNanos = %d, want %d", got, want)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./cmd/pg-router/... -run TestListenerCountObserver_OnAccept -v`
Expected: FAIL — `listenerCountObserver` has no `now` field yet.

- [ ] **Step 7: Implement the clock seam and recording**

In `cmd/pg-router/run.go` (`run.go:708-722`):

```go
type listenerCountObserver struct {
	counts map[string]*core.ListenerCounts
	// now is a clock seam (this task), defaulting to time.Now in
	// newListenerCountObserver -- overridable in tests for a deterministic
	// LastDeliveredAtNanos assertion.
	now func() time.Time
}

func newListenerCountObserver(counts map[string]*core.ListenerCounts) *listenerCountObserver {
	return &listenerCountObserver{counts: counts, now: time.Now}
}

func (l *listenerCountObserver) OnEnqueue(eventqueue.Event) {}

func (l *listenerCountObserver) OnAccept(_, listenerID string) {
	if c := l.counts[listenerID]; c != nil {
		c.Delivered.Add(1)
		c.LastDeliveredAtNanos.Store(l.now().UnixNano())
	}
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./cmd/pg-router/... -v`
Expected: PASS

- [ ] **Step 9: Write the failing TUI tests**

```go
// internal/tui/panes_test.go
func TestListener_DeclinedBucketed_KnownReasons(t *testing.T) {
	l := Listener{Declined: 3, DeclinedByReason: map[string]int64{"busy": 2, "unavailable": 1}}
	busy, unavailable, other := l.DeclinedBucketed()
	if busy != 2 || unavailable != 1 || other != 0 {
		t.Fatalf("DeclinedBucketed() = (%d,%d,%d), want (2,1,0)", busy, unavailable, other)
	}
}

func TestListener_DeclinedBucketed_OverrideStringFoldsIntoOther(t *testing.T) {
	l := Listener{Declined: 2, DeclinedByReason: map[string]int64{"busy": 1, "at-capacity": 1}}
	busy, unavailable, other := l.DeclinedBucketed()
	if busy != 1 || unavailable != 0 || other != 1 {
		t.Fatalf("DeclinedBucketed() = (%d,%d,%d), want (1,0,1)", busy, unavailable, other)
	}
	if busy+unavailable+other != l.Declined {
		t.Fatalf("bucketed sum %d != Declined %d", busy+unavailable+other, l.Declined)
	}
}

func TestListener_DeclinedBucketed_SumInvariantHoldsEvenOnCollidingOverrideText(t *testing.T) {
	// A DeclineDetail override that happens to equal "busy" verbatim is
	// indistinguishable from a genuine DeclineBusy at this layer (documented
	// ambiguity, spec's Review Focus) -- the invariant this test actually
	// guarantees is the sum, not which bucket it lands in.
	l := Listener{Declined: 5, DeclinedByReason: map[string]int64{"busy": 5}}
	busy, unavailable, other := l.DeclinedBucketed()
	if busy+unavailable+other != l.Declined {
		t.Fatalf("bucketed sum %d != Declined %d", busy+unavailable+other, l.Declined)
	}
}

func TestRenderListenersPane_NeverDispatchedRoleRendersCleanZeroState(t *testing.T) {
	// Review Focus: a declared role with zero delivered/declined, no
	// self-report, must render clean placeholders, never a blank cell.
	listeners := []Listener{{Role: "idle-role", Enabled: true}}
	out := renderListenersPane(listeners, render.TierWide, 0, render.Theme{}, "", "Listeners", nil)
	if !strings.Contains(out, "0 / 0 / 0") {
		t.Fatalf("expected a zero decline breakdown, got:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Fatalf("expected an em-dash for never-self-reported SELF, got:\n%s", out)
	}
	if !strings.Contains(out, "-") {
		t.Fatalf("expected a dash for never-delivered LAST DELIVERED, got:\n%s", out)
	}
}
```

- [ ] **Step 10: Run test to verify it fails**

Run: `go test ./internal/tui/... -run DeclinedBucketed -v`
Expected: FAIL — `Listener` has no `DeclinedByReason` field or `DeclinedBucketed` method yet.

- [ ] **Step 11: Implement in reply.go and panes.go**

In `internal/tui/reply.go`, widen `Listener` (`reply.go:143-151`):

```go
type Listener struct {
	Role      string   `json:"role"`
	Binds     []string `json:"binds"`
	Enabled   bool     `json:"enabled"`
	Excluded  bool     `json:"excluded"`
	Delivered int64    `json:"delivered"`
	// Declined stays the existing flat total (backward compatible).
	Declined int64 `json:"declined"`
	// LastDeliveredAtMs is Unix millis of the most recent delivery (this
	// task); 0 means never delivered.
	LastDeliveredAtMs int64 `json:"lastDeliveredAtMs"`
	// DeclinedByReason decodes the wire's existing declinedByReason object
	// (this task is the first decoder of it) -- keys are DeclineReason's
	// own text or an arbitrary DeclineDetail override.
	DeclinedByReason map[string]int64 `json:"declinedByReason"`
	Backoff          *Backoff         `json:"backoff"`
	// SelfReportState is this task's fold-in of the retired Registry pane:
	// the Registration.State whose ID equals this Listener's Role, or ""
	// if this listener has never self-reported.
	SelfReportState string `json:"selfReportState"`
}

// DeclinedBucketed buckets DeclinedByReason into (busy, unavailable,
// other). Any key other than the two known DeclineReason strings --
// including a DeclineDetail override -- folds into other, so
// busy+unavailable+other always sums to len(DeclinedByReason)'s total,
// which always sums to Declined (core.go's own invariant, ListenerCounts'
// doc).
func (l Listener) DeclinedBucketed() (busy, unavailable, other int64) {
	for reason, n := range l.DeclinedByReason {
		switch reason {
		case "busy":
			busy += n
		case "unavailable":
			unavailable += n
		default:
			other += n
		}
	}
	return busy, unavailable, other
}
```

In `internal/tui/panes.go`, add a self-report styling helper next to `poolDegraded` (`panes.go:66-76`):

```go
// selfReportDegraded reports whether state (a Listener's own
// SelfReportState) is one of the two states poolDegraded already treats as
// unhealthy at the pool axis -- reused here so a listener's OWN self-report
// gets the identical warning treatment, not a silently plainer rendering.
func selfReportDegraded(state string) bool {
	return state == "degraded" || state == "unavailable"
}
```

Replace `renderListenersPane` (`panes.go:215-250`) — widen the Wide/Narrow column sets, drop the flat `DECL` in favor of the three-way breakdown plus `SELF`, keep Tiny unchanged (it already omits DECL):

```go
func renderListenersPane(listeners []Listener, tier, width int, theme render.Theme, emptyMsg, title string, unmatchedBindings []string) string {
	var headers []string
	var widths []int
	switch tier {
	case render.TierWide:
		headers, widths = []string{"ROLE", "BINDS", "HEALTH", "LAST DELIVERED", "DLVD", "DECL(busy/unavail/other)", "SELF"}, []int{10, 14, 16, 14, 6, 14, 10}
	case render.TierNarrow:
		headers, widths = []string{"ROLE", "HEALTH", "LAST DELIVERED", "DLVD", "DECL(busy/unavail/other)"}, []int{10, 16, 14, 6, 14}
	default:
		headers, widths = []string{"ROLE", "HEALTH", "DLVD"}, []int{10, 14, 6}
	}

	_, perRow := unmatchedPartners(unmatchedBindings, listeners)
	rows := make([][]string, 0, len(listeners))
	for i, l := range listeners {
		role := textsafe.Sanitize(l.Role)
		health := listenerHealthText(l, theme)
		dlvd := fmt.Sprintf("%d", l.Delivered)
		busy, unavailable, other := l.DeclinedBucketed()
		decl := fmt.Sprintf("%d / %d / %d", busy, unavailable, other)
		lastDelivered := "-"
		if l.LastDeliveredAtMs > 0 {
			lastDelivered = formatCoarse(time.Since(time.UnixMilli(l.LastDeliveredAtMs))) + " ago"
		}
		self := "—"
		if l.SelfReportState != "" {
			self = textsafe.Sanitize(l.SelfReportState)
			if selfReportDegraded(l.SelfReportState) {
				self = theme.Cooling.Render(self)
			}
		}
		var row []string
		switch tier {
		case render.TierWide:
			binds := textsafe.Sanitize(strings.Join(l.Binds, ","))
			row = []string{role, binds, health, lastDelivered, dlvd, decl, self}
		case render.TierNarrow:
			row = []string{role, health, lastDelivered, dlvd, decl}
		default:
			row = []string{role, health, dlvd}
		}
		if types := perRow[i]; len(types) > 0 {
			row = append(row, unmatchedRowMarker(types, theme))
		}
		rows = append(rows, row)
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg, width)
}
```

Remove `renderRegistryPane` entirely (`panes.go:286-305`) — it has no remaining caller after Step 12.

- [ ] **Step 12: Remove the Registry pane from the zone loop**

In `internal/tui/model.go`:

- Remove `paneRegistry` from the loop `for _, p := range []int{paneListeners, paneQueues, paneSources, paneRegistry}` (`model.go:429`), leaving `[]int{paneListeners, paneQueues, paneSources}`.
- Remove the `if p == paneRegistry && ...` skip block (`model.go:430-437`) — no longer reachable.
- Remove the `case paneRegistry:` branch from `renderPaneContent` (`model.go:493-496`), `paneName` (around `model.go:512-513`), and `paneTitle` (around `model.go:527-528`).
- Remove `paneRegistry` from `unfocusedPaneDropOrder`'s switch (`model.go:540-547`) — Task 4 rewrites this function's body anyway; if Task 2 lands first, leave `case paneQueues: return 4` as the sole case in that switch's non-default arm for now.

- [ ] **Step 13: Run test to verify it passes**

Run: `go test ./internal/tui/... ./internal/core/... ./cmd/pg-router/... -v`
Expected: PASS. Also `go build ./...` to catch any remaining reference to `renderRegistryPane`/`paneRegistry` (e.g. in `zones_test.go` or `panes_test.go`'s own existing Registry-pane tests — update or remove those alongside this change).

- [ ] **Step 14: Update the JSON schema and golden fixture**

Add to `schemas/cli.status-reply.schema.json`'s `listeners[]` item `properties` (after `"declinedByReason"`, line 122) — both optional, not in `required`:

```json
          "lastDeliveredAtMs": { "type": "integer", "minimum": 0 },
          "selfReportState": { "type": "string" },
```

Add the same two keys (representative values, e.g. `0` and `""`) to `conformance/testdata/golden/cli.status-reply.json`'s `listeners[]` entries, matching Task 1 Step 12's pattern. Do **NOT** touch `conformance/testdata/compat/cli.status-reply.json` (same reason as Task 1 Step 12). Run `go test ./conformance/... -v` to confirm.

- [ ] **Step 15: Commit**

```bash
git add internal/core/core.go internal/core/status_test.go cmd/pg-router/run.go cmd/pg-router/run_test.go \
  internal/tui/reply.go internal/tui/panes.go internal/tui/model.go internal/tui/panes_test.go \
  schemas/cli.status-reply.schema.json \
  conformance/testdata/golden
git commit -m "feat(tui): fold self-report into Listeners, decode decline-reason breakdown, retire Registry pane"
```

---

## Task 3: Per-listener handler-failure counter

**Files:**

- Modify: `internal/orchestrator/listener.go` — `HandlerFailureObserver.OnHandlerFailure` interface signature widened with `listenerID` (the interface is declared in `listener.go:83-85`, NOT `orchestrator.go` — verified by direct read; `orchestrator.go` was an incorrect citation in an earlier draft of this plan) — and the call site (`listener.go:310-312`) passes `l.role.Name`
- Modify: `internal/metrics/metrics.go` — implementer signature updated (parameter added, existing behavior unchanged)
- Modify: `cmd/pg-router/run.go` — new `handlerFailureCountObserver`, fan-out wiring, `ListenerCounts.HandlerFailures`
- Modify: `internal/core/core.go` — `ListenerCounts.HandlerFailures`, `statusListeners` row key
- Modify: `internal/tui/reply.go` — `Listener.HandlerFailures`
- Modify: `internal/tui/panes.go` — `renderListenersPane` gains a `FAIL` column
- Modify: `schemas/cli.status-reply.schema.json` — declare `listeners[].handlerFailures` (same `additionalProperties: false` reason as Tasks 1-2)
- Test: `internal/orchestrator/listener_test.go`, `cmd/pg-router/run_test.go`, `internal/core/status_test.go`, `internal/tui/panes_test.go`

**Interfaces:**

- Consumes: nothing from Tasks 1-2 beyond `renderListenersPane`'s existing column set (this task adds one more column to the same function Task 2 already widened).
- Produces: `Listener.HandlerFailures int64`, rendered by this task only.

- [ ] **Step 1: Write the failing orchestrator test**

```go
// internal/orchestrator/listener_test.go
type recordingHandlerFailureObserver struct {
	eventID, evtType, listenerID string
	calls                        int
}

func (r *recordingHandlerFailureObserver) OnHandlerFailure(eventID, evtType, listenerID string) {
	r.eventID, r.evtType, r.listenerID = eventID, evtType, listenerID
	r.calls++
}

func TestRoleListener_Offer_HandlerFailurePassesListenerID(t *testing.T) {
	obs := &recordingHandlerFailureObserver{}
	o := &Orchestrator{ /* construct with whatever this file's existing tests already use to make workOne return a genuine non-busy error -- grep this file's existing HandlerFailureObserver test, if one exists, for the exact fixture/fake wireclient it uses */ HandlerFailureObserver: obs}
	l := o.NewListener(context.Background(), roles.Role{Name: "df-feedback", Binds: []string{"x"}})

	_ = l.Offer(eventqueue.Offering{ID: "dsp-1", Event: eventqueue.Event{ID: "evt-1", Type: "x"}})

	if obs.calls != 1 {
		t.Fatalf("OnHandlerFailure calls = %d, want 1", obs.calls)
	}
	if obs.listenerID != "df-feedback" {
		t.Fatalf("listenerID = %q, want %q", obs.listenerID, "df-feedback")
	}
}
```

NOTE: this file almost certainly already has a test exercising `HandlerFailureObserver` from before this task (bead `pg2-97539` added the hook) — find it first and extend/copy its existing fixture setup for constructing an `Orchestrator` whose `workOne` returns a non-busy, non-nil error, rather than inventing a new one from scratch.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/orchestrator/... -run TestRoleListener_Offer_HandlerFailurePassesListenerID -v`
Expected: FAIL (compile error) — `OnHandlerFailure` does not take a third parameter yet.

- [ ] **Step 3: Implement the signature widening**

In `internal/orchestrator/listener.go` (`listener.go:83-85` — NOT `orchestrator.go`, which has no such interface):

```go
type HandlerFailureObserver interface {
	// OnHandlerFailure fires as documented above. listenerID is the role
	// name (roleListener.ID()) that produced the failure -- added this task
	// so a per-listener failure count can be recorded, mirroring
	// eventqueue.Observer.OnDeclined's own listenerID parameter.
	OnHandlerFailure(eventID, evtType, listenerID string)
}
```

In `internal/orchestrator/listener.go` (`listener.go:310-312`):

```go
	if err != nil && l.handlerFailureObs != nil {
		l.handlerFailureObs.OnHandlerFailure(evt.ID, evt.Type, l.role.Name)
	}
```

In `internal/metrics/metrics.go`, find `OnHandlerFailure` and add the parameter to its signature, leaving its body's behavior unchanged (it does not need to use `listenerID` for the pool-wide metric it already emits):

```go
func (e *Emitter) OnHandlerFailure(eventID, evtType, listenerID string) {
	// existing body unchanged
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/orchestrator/... ./internal/metrics/... -v`
Expected: PASS. `go build ./...` will also surface any other implementer of this interface to update.

- [ ] **Step 5: Write the failing run.go wiring test**

```go
// cmd/pg-router/run_test.go
func TestHandlerFailureCountObserver_OnHandlerFailure_BumpsNamedListener(t *testing.T) {
	counts := map[string]*core.ListenerCounts{"df-feedback": {}}
	obs := &handlerFailureCountObserver{counts: counts}

	obs.OnHandlerFailure("evt-1", "x", "df-feedback")

	if got := counts["df-feedback"].HandlerFailures.Load(); got != 1 {
		t.Fatalf("HandlerFailures = %d, want 1", got)
	}
}

func TestFanOutHandlerFailureObserver_CallsEveryObserver(t *testing.T) {
	a, b := &recordingHandlerFailureObserver{}, &recordingHandlerFailureObserver{}
	f := fanOutHandlerFailureObserver{a, b}

	f.OnHandlerFailure("evt-1", "x", "df-feedback")

	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls = (%d,%d), want (1,1)", a.calls, b.calls)
	}
}
```

(`recordingHandlerFailureObserver` here is `cmd/pg-router`'s own test-local copy — it cannot import the one from `internal/orchestrator/listener_test.go`'s `_test.go` file across packages; redeclare the same three-line fake in this package's own test file.)

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./cmd/pg-router/... -run "HandlerFailureCountObserver|FanOutHandlerFailureObserver" -v`
Expected: FAIL — neither type exists yet.

- [ ] **Step 7: Implement in core.go and run.go**

In `internal/core/core.go`, add to `ListenerCounts` (alongside `LastDeliveredAtNanos` from Task 2):

```go
	// HandlerFailures counts genuine business-logic rejections
	// (HandlerFailureObserver) -- NOT a decline, since the item was
	// accepted. Bumped by cmd/pg-router's handlerFailureCountObserver.
	HandlerFailures atomic.Int64
```

Add `"handlerFailures": func() int64 { if c := counts[r.Name]; c != nil { return c.HandlerFailures.Load() }; return 0 }(),` as a new key in `statusListeners`'s per-row map (`core.go`, the same map literal Task 2 Step 3 widened) — or, more idiomatically, compute it alongside `delivered`/`declined` in the existing `if c := counts[r.Name]; c != nil { ... }` block Task 2 already introduces, adding one more local (`handlerFailures`) and one more map key.

In `cmd/pg-router/run.go`, add the two new types near `listenerCountObserver` (`run.go:699-722`):

```go
// handlerFailureCountObserver implements orchestrator.HandlerFailureObserver
// to bump a per-role handler-failure tally (this task), the same pattern
// listenerCountObserver already uses for delivered/declined.
type handlerFailureCountObserver struct {
	counts map[string]*core.ListenerCounts
}

func (h *handlerFailureCountObserver) OnHandlerFailure(_, _, listenerID string) {
	if c := h.counts[listenerID]; c != nil {
		c.HandlerFailures.Add(1)
	}
}

// fanOutHandlerFailureObserver calls every observer in order -- this task's
// counterpart to eventqueue's own fanOutObserver, needed because
// o.HandlerFailureObserver was previously a single assignment (emitter
// alone), and this task adds a second, independent consumer of the same
// signal.
type fanOutHandlerFailureObserver []orchestrator.HandlerFailureObserver

func (f fanOutHandlerFailureObserver) OnHandlerFailure(eventID, evtType, listenerID string) {
	for _, o := range f {
		o.OnHandlerFailure(eventID, evtType, listenerID)
	}
}
```

Move the existing assignment `o.HandlerFailureObserver = emitter` (`run.go:253`) to immediately after `listenerCounts` is built (after `run.go:282`), replacing it with:

```go
	o.HandlerFailureObserver = fanOutHandlerFailureObserver{emitter, &handlerFailureCountObserver{counts: listenerCounts}}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./cmd/pg-router/... ./internal/core/... -v`
Expected: PASS

- [ ] **Step 9: Write the failing TUI test and implement the FAIL column**

```go
// internal/tui/panes_test.go
func TestRenderListenersPane_WideTierIncludesFailColumn(t *testing.T) {
	listeners := []Listener{{Role: "df-feedback", HandlerFailures: 3}}
	out := renderListenersPane(listeners, render.TierWide, 0, render.Theme{}, "", "Listeners", nil)
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "3") {
		t.Fatalf("rendered pane missing FAIL column/value:\n%s", out)
	}
}

// TestRenderListenersPane_NeverFailedRoleShowsCleanZero closes the Review
// Focus gap the earlier plan review found: a never-dispatched/never-failed
// role must render "0" in FAIL, not a blank cell.
func TestRenderListenersPane_NeverFailedRoleShowsCleanZero(t *testing.T) {
	listeners := []Listener{{Role: "idle-role", Enabled: true}}
	out := renderListenersPane(listeners, render.TierWide, 0, render.Theme{}, "", "Listeners", nil)
	if !strings.Contains(out, "0") {
		t.Fatalf("expected a clean 0 in FAIL for a never-failed role, got:\n%s", out)
	}
}
```

Run: `go test ./internal/tui/... -run "TestRenderListenersPane_WideTierIncludesFailColumn|TestRenderListenersPane_NeverFailedRoleShowsCleanZero" -v` — expect FAIL (no such field/column yet).

Implement: add `HandlerFailures int64 \`json:"handlerFailures"\``to`Listener`in`reply.go`, and add a `"FAIL"`header +`fmt.Sprintf("%d", l.HandlerFailures)`cell to`renderListenersPane`'s Wide and Narrow column sets (both `headers`/`widths`and each`row` literal Task 2 Step 11 introduced).

Re-run — expect PASS.

- [ ] **Step 10: Update the JSON schema and golden fixture**

Add to `schemas/cli.status-reply.schema.json`'s `listeners[]` item `properties` (alongside Task 2's additions), optional:

```json
          "handlerFailures": { "type": "integer", "minimum": 0 },
```

Add `"handlerFailures": 0` to `conformance/testdata/golden/cli.status-reply.json`'s `listeners[]` entries. Do **NOT** touch `conformance/testdata/compat/cli.status-reply.json`. Run `go test ./conformance/... -v` to confirm.

- [ ] **Step 11: Commit**

```bash
git add internal/orchestrator/listener.go internal/orchestrator/listener_test.go \
  internal/metrics/metrics.go \
  internal/core/core.go \
  cmd/pg-router/run.go cmd/pg-router/run_test.go \
  internal/tui/reply.go internal/tui/panes.go internal/tui/panes_test.go \
  schemas/cli.status-reply.schema.json \
  conformance/testdata/golden
git commit -m "feat(tui): track and render per-listener handler-failure counts"
```

---

## Task 4: Two-tier layout (static above dynamic)

**Files:**

- Modify: `internal/tui/model.go` — `renderMain`'s zone list, `unfocusedPaneDropOrder`
- Test: `internal/tui/model_test.go` or `internal/tui/zones_test.go` (grep to confirm which file already tests zone ordering/drop behavior — this codebase's doc comments reference `zones_test.go` exercising "the SAME drop-order/pinned rules")

**Interfaces:**

- Consumes: `zoneSpec{name, content, pinned, dropOrder, fill, renderFill}` (existing type, unchanged shape).
- Produces: nothing new for later tasks — this is purely a reordering.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/model_test.go (or zones_test.go, whichever already owns zone-order assertions)
func TestRenderMain_StaticTierPositionUnaffectedByActivitySize(t *testing.T) {
	base := func(activityEntries int) *Model {
		m := newTestModel(nil) // real signature is newTestModel(p Poller); model_test.go:44 already calls it as newTestModel(nil)
		activity := make([]ActivityEntry, activityEntries)
		for i := range activity {
			activity[i] = ActivityEntry{Seq: uint64(i), Type: "x", Outcome: "delivered"}
		}
		m.reply = StatusReply{
			Listeners: []Listener{{Role: "df-feedback"}},
			Sources:   []Source{{Name: "pr-sweep"}},
			Activity:  activity,
		}
		return m
	}

	small := base(1).renderMain()
	large := base(20).renderMain()

	listenersLineSmall := lineContaining(small, "Listeners")
	listenersLineLarge := lineContaining(large, "Listeners")
	if listenersLineSmall != listenersLineLarge {
		t.Fatalf("Listeners pane moved from line %d to %d as Activity grew", listenersLineSmall, listenersLineLarge)
	}
}

// TestUnfocusedPaneDropOrder_StaticTierNeverDropsBeforeDynamicTier is a
// direct unit test on the pure ordering function, rather than driving
// layoutZones through an actual height-constrained render — Review Focus:
// under height pressure, Activity and Queues must be fully gone before
// Listeners or Sources drops even one row.
func TestUnfocusedPaneDropOrder_StaticTierNeverDropsBeforeDynamicTier(t *testing.T) {
	if unfocusedPaneDropOrder(paneActivity) >= unfocusedPaneDropOrder(paneQueues) {
		t.Fatalf("Activity (%d) must drop before Queues (%d)", unfocusedPaneDropOrder(paneActivity), unfocusedPaneDropOrder(paneQueues))
	}
	if unfocusedPaneDropOrder(paneQueues) >= unfocusedPaneDropOrder(paneListeners) {
		t.Fatalf("Queues (%d) must drop before Listeners (%d)", unfocusedPaneDropOrder(paneQueues), unfocusedPaneDropOrder(paneListeners))
	}
	if unfocusedPaneDropOrder(paneQueues) >= unfocusedPaneDropOrder(paneSources) {
		t.Fatalf("Queues (%d) must drop before Sources (%d)", unfocusedPaneDropOrder(paneQueues), unfocusedPaneDropOrder(paneSources))
	}
}

// TestRenderMain_ExtremeHeightPressureDropsDynamicTierBeforeStatic drives the
// REAL layoutZones algorithm through renderMain (not just the pure ordering
// function above) at a deliberately short m.height, closing the gap an
// earlier plan review found: the pure-function test alone cannot catch a bug
// in layoutZones' own height-budget logic.
func TestRenderMain_ExtremeHeightPressureDropsDynamicTierBeforeStatic(t *testing.T) {
	m := newTestModel(nil)
	m.reply = StatusReply{
		Listeners: []Listener{{Role: "df-feedback"}},
		Sources:   []Source{{Name: "pr-sweep"}},
		Queues:    []Queue{{Type: "pr.changed", Depth: 1}},
		Activity:  []ActivityEntry{{Seq: 1, Type: "x"}},
	}
	m.height = 6 // deliberately too short for every zone to fit

	out := m.renderMain()

	if strings.Contains(out, "Listeners") == false {
		t.Fatalf("static tier (Listeners) must survive extreme height pressure, got:\n%s", out)
	}
	if strings.Contains(out, "Activity") && strings.Contains(out, "Queues") {
		t.Fatalf("expected at least one dynamic-tier pane already dropped before the static tier would ever drop, got:\n%s", out)
	}
}
```

(If `m.height`/`newTestModel`'s exact field/zero-value behavior differs from this sketch — e.g. height is set via a different field or a `WindowSizeMsg` update rather than direct assignment — adapt to however this file's OTHER existing height-pressure tests, if any, already drive a short terminal; grep `model_test.go`/`zones_test.go` for an existing short-height test first.)

NOTE: `newTestModel`/`lineContaining` are almost certainly not the exact names already in use — grep `internal/tui/*_test.go` for however this package already constructs a `*Model` for a render test and locates a substring's line number, and use those instead.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/... -run "TestRenderMain_StaticTierPositionUnaffectedByActivitySize|TestUnfocusedPaneDropOrder_StaticTierNeverDropsBeforeDynamicTier|TestRenderMain_ExtremeHeightPressureDropsDynamicTierBeforeStatic" -v`
Expected: FAIL — today's zone order puts Activity above Listeners (so growing Activity DOES move the Listeners line), `paneActivity` does not exist yet, and today's drop order (Registry/Queues at 4, everything else at 5) does not guarantee Activity/Queues fully clear before Listeners/Sources drop.

- [ ] **Step 3: Implement**

In `internal/tui/model.go`, reorder `renderMain`'s zone assembly (`model.go:414-450`) so the static tier (Listeners, Sources) is appended BEFORE the Activity zone, and Queues moves to just before Activity (immediately after the static tier, still part of the dynamic tier conceptually but rendered directly above Activity):

```go
	zones := []zoneSpec{
		{name: "top", content: top, pinned: true},
	}
	if a := attentionLine(m.reply, m.clientVersion, m.theme); a != "" {
		zones = append(zones, zoneSpec{name: "attention", content: a, dropOrder: 1})
	}
	if p := pollErrorZone(m.pollErrFlagged, m.lastErr, m.theme); p != "" {
		zones = append(zones, zoneSpec{name: "poll-error", content: p, dropOrder: 2})
	}

	// Static tier: Listeners, Sources -- fixed membership for the run, never
	// reordered/resized by the dynamic tier below (this task).
	for _, p := range []int{paneListeners, paneSources} {
		content := m.renderPaneContent(p, gated, now)
		if p == m.focusedPane {
			zones = append(zones, zoneSpec{name: paneName(p), fill: true, renderFill: func(int) string { return content }})
			continue
		}
		zones = append(zones, zoneSpec{name: paneName(p), content: content, dropOrder: unfocusedPaneDropOrder(p)})
	}

	// Dynamic tier: Queues, then Activity -- both change every poll.
	if p := paneQueues; true {
		content := m.renderPaneContent(p, gated, now)
		if p == m.focusedPane {
			zones = append(zones, zoneSpec{name: paneName(p), fill: true, renderFill: func(int) string { return content }})
		} else {
			zones = append(zones, zoneSpec{name: paneName(p), content: content, dropOrder: unfocusedPaneDropOrder(p)})
		}
	}
	zones = append(zones, zoneSpec{
		name:      "activity",
		content:   m.renderActivityZoneContent(gated),
		dropOrder: unfocusedPaneDropOrder(paneActivity),
	})

	zones = append(zones, zoneSpec{name: "footer", content: footer, pinned: true})

	return layoutZones(zones, render.EffectiveWidth(m.width), m.height)
```

(This drops `paneRegistry` from the loop entirely — if Task 2 has not yet landed when this task is implemented, keep a `paneRegistry` zone in the dynamic tier for now and remove it when Task 2 lands; if Task 2 has already landed, there is nothing further to remove here.)

Add a `paneActivity` constant alongside the existing `paneListeners`/`paneQueues`/`paneSources` constants (wherever those are declared — likely `model.go` near their first use), purely so `unfocusedPaneDropOrder` can be extended uniformly:

```go
const paneActivity = -1 // not a real config-derived pane; used only to give Activity a dropOrder via the same function
```

Replace `unfocusedPaneDropOrder` (`model.go:540-547`):

```go
// unfocusedPaneDropOrder gives the dynamic tier a concrete drop sequence
// under height pressure (this task): Activity drops FIRST (it is the least
// load-bearing — an operator can always re-check activity on the next
// poll), then Queues. The static tier (Listeners, Sources) has no entry
// here at all after this task -- see renderMain, which no longer calls this
// function for either of them...
```

Wait — the static tier zones ABOVE still call `unfocusedPaneDropOrder(p)` for `paneListeners`/`paneSources` in the loop this step just wrote. Correct the doc and the function body so the static tier's two panes get the LOWEST drop priority (last to drop) and the dynamic tier's two get progressively higher priority (first to drop), preserving today's "lower number drops first" convention (`model.go:540-547`'s existing 4-vs-5 split):

```go
// unfocusedPaneDropOrder ranks every non-focused pane's drop priority under
// height pressure (this task's two-tier redesign): Activity drops first (1),
// then Queues (2); Listeners/Sources -- the static tier -- never drop before
// both dynamic-tier panes are already gone (3).
func unfocusedPaneDropOrder(p int) int {
	switch p {
	case paneActivity:
		return 1
	case paneQueues:
		return 2
	default:
		return 3
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/... -v`
Expected: PASS. Also re-run any EXISTING drop-order test this package already had for the old 4/5 split (grep `unfocusedPaneDropOrder` in `*_test.go`) and update its expected numbers to match the new 1/2/3 scheme rather than leaving it red.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/zones_test.go
git commit -m "feat(tui): render Listeners/Sources above Queues/Activity so activity growth never reflows them"
```

---

## Task 5: Queues pane depth bar and pr.reconcile relabeling

**Files:**

- Modify: `internal/tui/panes.go` — `renderQueuesPane`
- Test: `internal/tui/panes_test.go`

**Interfaces:**

- Consumes: `Queue{Type string, Depth int}` (existing, unchanged).
- Produces: nothing new for later tasks.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/panes_test.go
func TestRenderQueuesPane_HeartbeatTypeHasNoBarButHasLabel(t *testing.T) {
	out := renderQueuesPane([]Queue{{Type: "pr.reconcile", Depth: 70}}, 0, "", "Queues")
	if strings.ContainsAny(out, "█░") {
		t.Fatalf("heartbeat queue row rendered a depth bar:\n%s", out)
	}
	if !strings.Contains(out, "(heartbeat)") {
		t.Fatalf("heartbeat queue row missing label:\n%s", out)
	}
}

func TestRenderQueuesPane_IncrementalTypeHasBarNoLabel(t *testing.T) {
	out := renderQueuesPane([]Queue{{Type: "pr.changed", Depth: 3}}, 0, "", "Queues")
	if !strings.Contains(out, "█") {
		t.Fatalf("incremental queue row missing a depth bar:\n%s", out)
	}
	if strings.Contains(out, "(heartbeat)") {
		t.Fatalf("incremental queue row should not carry the heartbeat label:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/... -run TestRenderQueuesPane -v`
Expected: FAIL — `renderQueuesPane` renders neither a bar nor a label today.

- [ ] **Step 3: Implement**

In `internal/tui/panes.go`, replace `renderQueuesPane` (`panes.go:255-263`):

```go
// heartbeatQueueTypes names queue types whose depth is a reconciliation
// heartbeat (a monitored-universe count), not an incremental/actionable
// backlog -- pr.reconcile today. pg-router has no per-type metadata to
// derive this from, so it is a hardcoded set; promote to config if a
// second heartbeat-style type is ever added.
var heartbeatQueueTypes = map[string]bool{
	"pr.reconcile": true,
}

// depthBar renders a coarse, fixed-width bar for depth out of an assumed
// max (this task uses 100 as a generous ceiling -- there is no configured
// per-type max to scale against, and a bar that never fills for a
// reasonable depth is more honest than a false sense of precision).
func depthBar(depth int) string {
	const width = 20
	const assumedMax = 100
	filled := depth * width / assumedMax
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func renderQueuesPane(queues []Queue, width int, emptyMsg, title string) string {
	headers := []string{"TYPE", "DEPTH"}
	widths := []int{18, 30}
	rows := make([][]string, 0, len(queues))
	for _, q := range queues {
		depthCell := fmt.Sprintf("%d", q.Depth)
		if heartbeatQueueTypes[q.Type] {
			depthCell += "  (heartbeat)"
		} else {
			depthCell += "  " + depthBar(q.Depth)
		}
		rows = append(rows, []string{textsafe.Sanitize(q.Type), depthCell})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg, width)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/panes.go internal/tui/panes_test.go
git commit -m "feat(tui): add a depth bar to incremental queues, relabel pr.reconcile as a heartbeat"
```

---

## Task 6: Activity pane cap and relative timestamps

**Files:**

- Modify: `internal/tui/panes.go` — `renderActivityPane`
- Test: `internal/tui/panes_test.go`

**Interfaces:**

- Consumes: `ActivityEntry{Seq uint64, StartedAt time.Time, Type, Outcome string}` (existing, unchanged — no listener attribution field is added, per the spec's explicit correction).
- Produces: nothing new for later tasks.

- [ ] **Step 1: Write the failing test**

```go
// internal/tui/panes_test.go
func TestRenderActivityPane_CapsToLastEightNewestFirst(t *testing.T) {
	now := time.Now()
	entries := make([]ActivityEntry, 12)
	for i := range entries {
		entries[i] = ActivityEntry{Seq: uint64(i), StartedAt: now.Add(time.Duration(i) * time.Second), Type: fmt.Sprintf("t%d", i)}
	}
	out := renderActivityPane(entries, false, "", render.Theme{})
	if strings.Contains(out, "t0") || strings.Contains(out, "t3") {
		t.Fatalf("expected the 4 oldest entries dropped, got:\n%s", out)
	}
	if !strings.Contains(out, "t11") {
		t.Fatalf("expected the newest entry present, got:\n%s", out)
	}
}

func TestRenderActivityPane_FewerThanCapDoesNotPanicOrPad(t *testing.T) {
	out := renderActivityPane([]ActivityEntry{{Seq: 1, StartedAt: time.Now(), Type: "x"}}, false, "", render.Theme{})
	// paneFrame (panes.go) emits exactly one line per content row plus a
	// top and bottom border line -- 1 activity entry means 3 total lines,
	// i.e. 2 newline separators. Padding toward activityDisplayCap would
	// add more; this asserts the exact count rather than "at least one
	// line," which would pass even if the implementation padded.
	if got := strings.Count(out, "\n"); got != 2 {
		t.Fatalf("expected exactly 2 newlines (1 row + top/bottom border, no padding) for a single entry, got %d:\n%s", got, out)
	}
}

func TestRenderActivityPane_RelativeTimestampNotAbsolute(t *testing.T) {
	out := renderActivityPane([]ActivityEntry{{Seq: 1, StartedAt: time.Now().Add(-5 * time.Second), Type: "x"}}, false, "", render.Theme{})
	if strings.Contains(out, ":") {
		t.Fatalf("expected a relative timestamp with no ':' (no HH:MM:SS), got:\n%s", out)
	}
	if !strings.Contains(out, "ago") {
		t.Fatalf("expected a relative 'ago' timestamp, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/... -run TestRenderActivityPane -v`
Expected: FAIL — today's rendering shows every entry with an absolute `HH:MM:SS` timestamp.

- [ ] **Step 3: Implement**

In `internal/tui/panes.go`, replace `renderActivityPane` (`panes.go:314-343`):

```go
// activityDisplayCap is the most recent N Activity entries actually
// rendered (this task) -- a Go constant, not configurable, for phase 1.
const activityDisplayCap = 8

func renderActivityPane(activity []ActivityEntry, dropped bool, emptyMsg string, theme render.Theme) string {
	rows := make([]string, 0, activityDisplayCap+1)
	if dropped {
		rows = append(rows, "(older entries dropped -- ring capacity exceeded)")
	}
	now := time.Now()
	shown := 0
	for i := len(activity) - 1; i >= 0 && shown < activityDisplayCap; i-- {
		a := activity[i]
		ts := "-"
		if !a.StartedAt.IsZero() {
			ts = formatCoarse(now.Sub(a.StartedAt)) + " ago"
		}
		line := fmt.Sprintf("%-10s %-10s", ts, textsafe.Sanitize(a.Type))
		if a.Outcome != "" {
			line += " → " + renderActivityOutcome(a.Outcome, theme)
		}
		rows = append(rows, line)
		shown++
	}
	return renderPaneBoxPlain("Activity", rows, emptyMsg)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/panes.go internal/tui/panes_test.go
git commit -m "feat(tui): cap Activity to the 8 most recent entries with relative timestamps"
```

---

## Task 7: docs/behavior/interfaces.md reconciliation

**Files:**

- Modify: `docs/behavior/interfaces.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Update the staleness section**

Find wherever `docs/behavior/interfaces.md` currently documents a source's staleness/health state (the section the spec's "Design overview" point 1 and Task 1 both reference) and correct it to state: staleness is evaluated against the source's OWN expected interval (explicit `expected_interval` override, or a `kind: "period"` query's resolved trigger interval), never the pool's tick cadence; a source with no known interval reports `N/A`, distinct from `stale`; a source that has never ticked reports `idle`, outranking both.

- [ ] **Step 2: Update the decline-reason section**

Add or correct the section describing `Listener`'s decline accounting to state that the wire's `declinedByReason` breakdown (busy/unavailable/other, where "other" includes any `DeclineDetail` override string) is now rendered by the TUI, not just carried on the wire — and that a resource-limit hard-stop is never counted as a decline (already true; document it if not already stated).

- [ ] **Step 3: Reconcile the Registry/self-report line**

Find `interfaces.md:234-236` ("a push-only source still registers so it appears in the registry and its lifecycle is known") and rewrite it: no source, push or pull, registers into the self-report registry today. A source's liveness is reported entirely through the Sources pane's own `lastTick`/staleness fields, not registry membership. Self-report registration stays handler-only; the Registry pane itself no longer exists as a separate operator-facing surface — self-report state, when present, renders as a column on the Listeners pane instead.

- [ ] **Step 4: Add the handler-failure signal**

Document that a handler's genuine business-logic rejection (`HandlerFailureObserver`) is tracked per listener and rendered on the Listeners pane's `FAIL` column, distinct from both decline reasons and from a dispatch-level failure.

- [ ] **Step 5: Commit**

```bash
git add docs/behavior/interfaces.md
git commit -m "docs(pg-router): reconcile interfaces.md with corrected staleness, decline, and registry behavior"
```
