package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/query"
)

func TestDecodeGlobalBudget_overlaysBudgetOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := "[pool]\nself_login = \"ignored\"\n[pool.budget]\ntokens = 500000\ntime = \"40m\"\n" +
		"[[role]]\nname = \"ignored-role\"\ntype = \"command\"\n[role.command]\nargv = [\"x\"]\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := NewRegistry().decodeGlobalBudget(p, &c); err != nil {
		t.Fatal(err)
	}
	if c.BudgetTokens != 500000 {
		t.Errorf("BudgetTokens = %d, want 500000", c.BudgetTokens)
	}
	if c.BudgetTime != 40*time.Minute {
		t.Errorf("BudgetTime = %v, want 40m", c.BudgetTime)
	}
	// Cost omitted in file => Default() (0/unlimited) preserved.
	if c.BudgetCost != 0 {
		t.Errorf("BudgetCost = %d, want 0 (unchanged)", c.BudgetCost)
	}
	// self_login and [[role]] must be IGNORED by the global layer (budget-only scope).
	if c.SelfLogin != "" {
		t.Errorf("SelfLogin = %q, want empty (global file must not set non-budget scalars)", c.SelfLogin)
	}
}

func TestDecodeGlobalBudget_malformedIsHardError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("this is = not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := NewRegistry().decodeGlobalBudget(p, &c); err == nil {
		t.Fatal("malformed global config must be a hard error")
	}
}

// --- retry cadence / pull-source failure backoff config surfaces (pg2-0c8yz) ---

// A role that sets no [role.retry] table inherits the POOL default verbatim —
// backoff.Default() when [pool].retry is also absent — so an existing config
// with no opinion on cadence is unaffected (INV-FAIL-2).
func TestLoad_roleRetryBackoffDefaultsToPoolDefault(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 1 {
		t.Fatalf("roles = %+v, want exactly one", c.Roles)
	}
	if got, want := c.Roles[0].RetryBackoff, backoff.Default(); got != want {
		t.Fatalf("RetryBackoff = %+v, want the pool default %+v", got, want)
	}
}

// A per-role [role.retry] table overlays only the fields it sets onto the
// pool-wide default (INV-FAIL-2): here only `factor`, leaving Initial/Max at
// the pool ([pool].retry) values.
func TestLoad_roleRetryBackoffOverlaysPoolDefault(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[pool.retry]
initial = "3s"
max = "90s"

[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.retry]
factor = 3
[role.command]
argv = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := c.Roles[0].RetryBackoff
	want := backoff.Policy{Initial: 3 * time.Second, Factor: 3, Max: 90 * time.Second}
	if got != want {
		t.Fatalf("RetryBackoff = %+v, want %+v (pool initial/max + role's own factor)", got, want)
	}
	// The pool-level Config field itself must carry the [pool].retry overlay too,
	// independent of any role.
	if c.RetryBackoff.Initial != 3*time.Second || c.RetryBackoff.Max != 90*time.Second {
		t.Fatalf("Config.RetryBackoff = %+v, want pool.retry applied", c.RetryBackoff)
	}
}

// An invalid [role.retry] (factor <= 1 cannot grow the wait) is a hard decode
// error, aggregated like every other per-role error.
func TestLoad_roleRetryBackoffInvalidFactorIsError(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.retry]
factor = 1
[role.command]
argv = ["x"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("factor <= 1 must be a hard error (it cannot grow the wait)")
	}
}

// A query with no [query.failure_backoff] table inherits the pool default
// verbatim — Retries: 0 (fail fast) when [pool].pull_failure_backoff is also
// absent, exactly today's behavior (INV-FAIL-3).
func TestLoad_queryFailureBackoffDefaultsToFailFast(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	q, ok := c.Queries[0].Query.(query.CommandQuery)
	if !ok {
		t.Fatalf("query is %T, want query.CommandQuery", c.Queries[0].Query)
	}
	fb := q.FailureBackoff()
	if fb.Retries != 0 {
		t.Fatalf("FailureBackoff.Retries = %d, want 0 (fail fast, unchanged default)", fb.Retries)
	}
	if fb.Policy != backoff.Default() {
		t.Fatalf("FailureBackoff.Policy = %+v, want the pool default %+v", fb.Policy, backoff.Default())
	}
}

// A per-query [query.failure_backoff] table overlays the pool-wide
// [pool].pull_failure_backoff default (INV-FAIL-3): the pool sets Retries: 2,
// the query overrides only `retries` to 5, and the shape (initial/factor/max)
// still comes from the pool.
func TestLoad_queryFailureBackoffOverlaysPoolDefault(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[pool.pull_failure_backoff]
initial = "2s"
factor = 4
max = "1m"
retries = 2

[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.failure_backoff]
retries = 5
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	q, ok := c.Queries[0].Query.(query.CommandQuery)
	if !ok {
		t.Fatalf("query is %T, want query.CommandQuery", c.Queries[0].Query)
	}
	fb := q.FailureBackoff()
	want := query.FailureBackoff{Policy: backoff.Policy{Initial: 2 * time.Second, Factor: 4, Max: time.Minute}, Retries: 5}
	if fb != want {
		t.Fatalf("FailureBackoff = %+v, want %+v (pool shape + query's own retries)", fb, want)
	}
	// The pool-level Config scalars must carry the overlay too.
	if c.PullFailureRetries != 2 {
		t.Fatalf("Config.PullFailureRetries = %d, want 2 (pool default, unaffected by the query override)", c.PullFailureRetries)
	}
}

// A negative `retries` is a hard decode error.
func TestLoad_queryFailureBackoffNegativeRetriesIsError(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.failure_backoff]
retries = -1
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("negative retries must be a hard error")
	}
}

// --- serialize-mark config surface (pg2-cl9jz, INV-CONC-1 / DEC-CONC-1) ---

// [pool].serialize_types decodes into Config.SerializeTypes verbatim.
func TestLoad_serializeTypesDecodesFromPool(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[pool]
serialize_types = ["shutdown", "time-of-day"]

[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.SerializeTypes, []string{"shutdown", "time-of-day"}) {
		t.Fatalf("SerializeTypes = %v, want [shutdown time-of-day]", c.SerializeTypes)
	}
}

// Absent [pool].serialize_types leaves Config.SerializeTypes empty — an
// existing deployment marks nothing and its dispatch is unchanged.
func TestLoad_serializeTypesAbsentLeavesItEmpty(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
binds = ["e"]
[role.command]
argv = ["x"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SerializeTypes) != 0 {
		t.Fatalf("SerializeTypes = %v, want empty (key absent)", c.SerializeTypes)
	}
}

// --- role-name charset/path-safety hardening (this bead, pg2-ymb3v) ---
//
// buildRole joins role.Name directly into a filesystem path when
// HandlerCommandDir is configured (filepath.Join(dir, role.Name+".json")),
// so a name containing "/" or ".." is rejected at config-DECODE time,
// independent of whether HandlerCommandDir is ever set for this deployment
// — the same "reject the footgun regardless of whether today's config uses
// the feature" posture buildRole already takes for other required fields.

// TestLoad_roleNameWithSlashIsError proves a role name containing "/" is
// rejected at config-decode time (acceptance criterion c).
func TestLoad_roleNameWithSlashIsError(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "feedback/worker"
binds = ["e"]
`)
	_, err := Load()
	if err == nil {
		t.Fatal("a role name containing '/' must be rejected at config-decode time")
	}
	if !strings.Contains(err.Error(), "feedback/worker") {
		t.Errorf("err = %q, want it to name the offending role %q", err, "feedback/worker")
	}
}

// TestLoad_roleNameWithDotDotIsError proves a role name containing ".." is
// rejected too (path-traversal, not just a bad subdirectory reference).
func TestLoad_roleNameWithDotDotIsError(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "../escape"
binds = ["e"]
`)
	_, err := Load()
	if err == nil {
		t.Fatal("a role name containing '..' must be rejected at config-decode time")
	}
}

// TestLoad_roleNameOrdinaryIsAccepted is the negative control: an ordinary
// role name (the feedback/worker/review shape the bead's own motivating
// example uses) must NOT be rejected by the new charset check.
func TestLoad_roleNameOrdinaryIsAccepted(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "feedback-worker"
binds = ["e"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "feedback-worker" {
		t.Fatalf("Roles = %+v, want one role named feedback-worker", c.Roles)
	}
}

// --- per-source expected tick cadence (this task, pg2-mnf7t.1) ---

// An explicit [[query]].expected_interval override always wins over a period
// trigger's own resolved `every`, even though the trigger's own interval is
// also known here (10s) — the override is authored specifically to declare
// staleness cadence and must take precedence.
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

// With no override, a kind:"period" query (the default kind) falls back to
// its own resolved trigger Every.
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

// A threshold (or manual) trigger with no explicit override has no cadence
// to derive from, so ExpectedIntervalMsFor returns 0 (unknown).
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
