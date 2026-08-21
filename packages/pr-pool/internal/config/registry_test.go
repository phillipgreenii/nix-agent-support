package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/query"
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
type = "beads-ready"
[query.beads-ready]
labels = ["a"]

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
type = "beads-ready"
[query.beads-ready]
labels = ["a"]

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
type = "beads-ready"
[query.beads-ready]
labels = ["a"]

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
type = "beads-ready"
[query.beads-ready]
labels = ["a"]

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
	q, ok := c.Queries[0].Query.(query.BeadsReady)
	if !ok {
		t.Fatalf("query is %T, want query.BeadsReady", c.Queries[0].Query)
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
type = "beads-ready"
[query.failure_backoff]
retries = 5
[query.beads-ready]
labels = ["a"]

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
	q, ok := c.Queries[0].Query.(query.BeadsReady)
	if !ok {
		t.Fatalf("query is %T, want query.BeadsReady", c.Queries[0].Query)
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
type = "beads-ready"
[query.failure_backoff]
retries = -1
[query.beads-ready]
labels = ["a"]

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
