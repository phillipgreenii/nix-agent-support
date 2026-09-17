package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// duration is a TOML-decodable time.Duration ("30m", "25m"), mirroring ccpool's
// Duration. Used for [pool].budget.time and per-role budget.time.
type duration struct{ D time.Duration }

func (d *duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.D = v
	return nil
}

// fileShape is the typed first-pass decode target. A single-bracket `[role]` table
// (the classic `[[role]]` typo) makes toml.Decode fail with a table-vs-array type
// mismatch, which surfaces as a hard error — no special detection needed.
type fileShape struct {
	Pool     poolTOML      `toml:"pool"`
	Roles    []roleTOML    `toml:"role"`
	Queries  []queryTOML   `toml:"query"`
	Monitors []monitorTOML `toml:"monitor"`
}

type poolTOML struct {
	SelfLogin   string      `toml:"self_login"`
	WorktreeDir string      `toml:"worktree_dir"`
	Budget      *budgetTOML `toml:"budget"`
	// Retry is the pool-wide DEFAULT handler retry cadence (INV-FAIL-2,
	// pg2-0c8yz), overlaid by a per-role [role.retry] table. Absent: the Go-level
	// default (backoff.Default()).
	Retry *backoffTOML `toml:"retry"`
	// PullFailureBackoff is the pool-wide DEFAULT pull-source failure backoff
	// (INV-FAIL-3), overlaid by a per-query [query.failure_backoff] table.
	// Absent: backoff.Default() shape with Retries: 0 (fail fast, unchanged from
	// today).
	PullFailureBackoff *failureBackoffTOML `toml:"pull_failure_backoff"`
	// SerializeTypes marks each named event TYPE to serialize (INV-CONC-1,
	// `packages/pg-router/docs/decisions · DEC-CONC-1`): the queue offers at most
	// one event of a marked type at a time, across every bound handler, until
	// it is released. Absent/empty: marks nothing (unchanged from today).
	SerializeTypes []string `toml:"serialize_types"`
	// OperatorPausedPath / CICDDownPath override the two INV-LIFE-2 gate file
	// paths `pause`/`resume` act on. Absent: PG_ROUTER_OPERATOR_PAUSED /
	// PG_ROUTER_CICD_DOWN env, then <LogDir>/gates/{operator-paused,cicd-down}
	// (config.Load()'s post-repo-TOML fill; config.GatePaths() resolves the
	// identical precedence without Load()).
	OperatorPausedPath string `toml:"operator_paused_path"`
	CICDDownPath       string `toml:"cicd_down_path"`
}

type budgetTOML struct {
	Tokens *int64    `toml:"tokens"`
	Cost   *int64    `toml:"cost"`
	Time   *duration `toml:"time"`
}

// backoffTOML is the shared retry-cadence SHAPE table (pg2-0c8yz): a short
// initial wait, growing by factor on each consecutive failure, capped at max.
// Used standalone for the handler retry cadence ([pool].retry / [role.retry])
// and duplicated (with retries) into failureBackoffTOML for the pull-source
// failure backoff.
type backoffTOML struct {
	Initial *duration `toml:"initial"`
	Factor  float64   `toml:"factor"`
	Max     *duration `toml:"max"`
}

// failureBackoffTOML is the pull-source failure backoff table
// ([pool].pull_failure_backoff / [query.failure_backoff], INV-FAIL-3): the same
// shape as backoffTOML plus Retries, the bound on how many further attempts
// discover.Produce makes within one pass before giving up.
type failureBackoffTOML struct {
	Initial *duration `toml:"initial"`
	Factor  float64   `toml:"factor"`
	Max     *duration `toml:"max"`
	Retries *int      `toml:"retries"`
}

type roleTOML struct {
	Name    string   `toml:"name"`
	Enabled *bool    `toml:"enabled"` // pointer: absent => default true
	Binds   []string `toml:"binds"`   // event types this role consumes (Observer)
	// Retry is this role's HANDLER RETRY CADENCE override (INV-FAIL-2,
	// pg2-0c8yz), overlaid onto the pool-wide default ([pool].retry). Absent:
	// inherits the pool default verbatim.
	Retry *backoffTOML `toml:"retry"`
}

// queryTOML is one top-level [[query]]: a named producer. It carries its config
// name, the event type(s) it emits (roles bind these), an optional firing
// trigger (default: period), the query type discriminator, and each query type's
// sub-table as a deferred-decode Primitive (the factory decodes the one matching
// `type`).
//
// Only `command` (an opaque token Core just invokes, never interprets — how the
// executable behaves is entirely the deploying flake's business) is typed in
// here. `beads-ready` / `beads-list` / `github-issues` / `jira-issues` were
// removed (pg2-n75tk): each one typed "how another tool is configured" into
// Core, which is exactly the boundary GOAL-MIN-1 forbids, and `jira-issues`
// specifically was structurally unsatisfiable — its backing command exists
// only in a downstream flake agent-support cannot legitimately depend on
// (INV-WORKFLOW-1 check 5 would refuse to load any config declaring it). See
// MIGRATION.md for converting an old `beads-ready` / `beads-list` /
// `github-issues` / `jira-issues` block to an equivalent `command` block. The
// spec-C-deferred `event` query type (a saga/correlation source) was
// registered under design M5 and later deleted outright (pg2-9d0he): its sole
// consumer, a role's opt-in correlation feature, had already been removed.
type queryTOML struct {
	Name    string         `toml:"name"`
	Emits   []string       `toml:"emits"`
	Trigger *triggerTOML   `toml:"trigger"`
	Type    string         `toml:"type"`
	Command toml.Primitive `toml:"command"`
	// FailureBackoff is this query's PULL-SOURCE FAILURE BACKOFF override
	// (INV-FAIL-3, pg2-0c8yz), overlaid onto the pool-wide default
	// ([pool].pull_failure_backoff). Absent: inherits the pool default verbatim
	// (Retries: 0 unless the pool default itself opts in).
	FailureBackoff *failureBackoffTOML `toml:"failure_backoff"`
}

// triggerTOML is a query's firing strategy (Q1). kind selects the concrete
// Strategy: "period" (default), "threshold", or "manual".
type triggerTOML struct {
	Kind  string    `toml:"kind"`
	Every *duration `toml:"every"` // period
	Count int       `toml:"count"` // threshold
	Binds []string  `toml:"binds"` // threshold: the upstream types to count
}

// monitorTOML is one top-level [[monitor]] entry (pg2-nhvdo, INTF-MON): the
// TOML surface for Config.MonitorSubsets, the `id -> subset` map
// `Service.Register` consults at register-time and `mon.read` filters
// against. id is the sink's own `register` id (INTF-MON: "the sink's own
// configured entry"); subset is the metric NAMEs that id may read — this
// package does not validate metric names against the catalog, mirroring how
// a role's `binds`/a query's `emits` event-type strings are opaque tokens to
// this layer too. There is no mode field: the push direction stays
// deliberately unrealized (bead `pg2-ov09n`, closed), so every declared
// sink is implicitly pull-only.
type monitorTOML struct {
	ID     string   `toml:"id"`
	Subset []string `toml:"subset"`
}

// Registry decodes a config file's roles and queries. It is instance-scoped (no
// package-level init() globals) — matching the codebase's constructor-injection
// convention. Adding a query type is one line in query.NewQueryFactories; adding a
// role type is one case in buildRole.
type Registry struct{ queries *query.Factories }

func NewRegistry() *Registry { return &Registry{queries: query.NewQueryFactories()} }

// decodeRoleSet decodes path: overlays its [pool] scalars onto c, then builds the
// RoleSet. Returns (nil, nil) when the file has no [[role]] array (pool-only or
// empty) to signal "use the built-in role set". All per-role errors are aggregated.
func (r *Registry) decodeRoleSet(path, configDir string, c *Config) (roles.RoleSet, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var shape fileShape
	md, err := toml.Decode(string(body), &shape)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if shape.Pool.SelfLogin != "" {
		c.SelfLogin = shape.Pool.SelfLogin
	}
	// [pool].worktree_dir overlays after the env overlay in Load(), so config
	// (repo) takes precedence over PG_ROUTER_WORKTREE_DIR (env, global). An absent
	// key leaves the env/default value intact.
	if shape.Pool.WorktreeDir != "" {
		c.WorktreeDir = shape.Pool.WorktreeDir
	}
	// [pool].operator_paused_path / cicd_down_path overlay after the env overlay in
	// Load() the same way worktree_dir does — config (repo) wins over
	// PG_ROUTER_OPERATOR_PAUSED / PG_ROUTER_CICD_DOWN (env), which already won over
	// Default()'s "". An absent key leaves the env/default value intact; Load()
	// fills either still-empty field from <LogDir>/gates/... AFTER this returns.
	if shape.Pool.OperatorPausedPath != "" {
		c.OperatorPaused = shape.Pool.OperatorPausedPath
	}
	if shape.Pool.CICDDownPath != "" {
		c.CICDDown = shape.Pool.CICDDownPath
	}
	overlayConfigBudget(c, shape.Pool.Budget)
	// serialize_types (INV-CONC-1, pg2-cl9jz): a present, non-empty list REPLACES
	// the default (empty — marks nothing); an absent/empty key leaves c's
	// existing value (Default()'s nil) untouched. There is no per-role/per-query
	// overlay for this one — see DEC-CONC-1's "not decided here".
	if len(shape.Pool.SerializeTypes) > 0 {
		c.SerializeTypes = shape.Pool.SerializeTypes
	}
	// Pool-wide retry-cadence defaults (INV-FAIL-2 / INV-FAIL-3, pg2-0c8yz) MUST
	// resolve before buildRole/buildQueries below, since both read c.RetryBackoff
	// / c.PullFailureBackoff / c.PullFailureRetries as their BASE to overlay a
	// per-role / per-query table onto.
	rb, err := buildBackoffPolicy(c.RetryBackoff, shape.Pool.Retry)
	if err != nil {
		return nil, fmt.Errorf("pool.retry: %w", err)
	}
	c.RetryBackoff = rb
	pfb, err := buildFailureBackoff(query.FailureBackoff{Policy: c.PullFailureBackoff, Retries: c.PullFailureRetries}, shape.Pool.PullFailureBackoff)
	if err != nil {
		return nil, fmt.Errorf("pool.pull_failure_backoff: %w", err)
	}
	c.PullFailureBackoff, c.PullFailureRetries = pfb.Policy, pfb.Retries
	// [[monitor]] resolves Config.MonitorSubsets (INTF-MON, pg2-nhvdo) — a
	// pool-level key like the overlays above, so it applies whether or not
	// [[role]]/[[query]] are declared (built before the roles-empty early
	// return below, unlike Roles/Queries themselves which the built-in
	// fallback owns when absent).
	if len(shape.Monitors) > 0 {
		subsets, err := buildMonitorSubsets(shape.Monitors)
		if err != nil {
			return nil, fmt.Errorf("monitor: %w", err)
		}
		c.MonitorSubsets = subsets
	}
	if len(shape.Roles) == 0 {
		return nil, nil // pool-only / empty => built-ins
	}
	var out roles.RoleSet
	var errs []error
	seen := map[string]bool{}
	for i, rt := range shape.Roles {
		role, err := r.buildRole(md, rt, configDir, *c)
		if err != nil {
			errs = append(errs, fmt.Errorf("role[%d] %q: %w", i, rt.Name, err))
			continue
		}
		if seen[role.Name] {
			errs = append(errs, fmt.Errorf("duplicate role name %q", role.Name))
			continue
		}
		seen[role.Name] = true
		out = append(out, role)
	}
	// Build the producer set from [[query]] (design M3). A config with [[role]]
	// but no [[query]] leaves c.Queries empty; Validate then flags every role's
	// Binds as an orphan consumer (a clear, aggregated diagnostic).
	queries, qerrs := r.buildQueries(md, shape.Queries, *c)
	errs = append(errs, qerrs...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	c.Queries = queries
	return out, nil
}

// buildQueries decodes every [[query]] into a named producer (query.Source),
// installing its emits + trigger. Duplicate query names are rejected.
func (r *Registry) buildQueries(md toml.MetaData, qts []queryTOML, c Config) (query.SourceSet, []error) {
	var out query.SourceSet
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
	}
	return out, errs
}

// buildMonitorSubsets decodes every [[monitor]] entry into the `id ->
// subset` map Config.MonitorSubsets carries (INTF-MON, pg2-nhvdo), mirroring
// buildQueries'/decodeRoleSet's own required-field + duplicate-name
// aggregation (an empty id or a repeated one is every entry's error,
// collected together rather than stopping at the first).
func buildMonitorSubsets(mts []monitorTOML) (map[string][]string, error) {
	out := map[string][]string{}
	var errs []error
	for i, mt := range mts {
		if mt.ID == "" {
			errs = append(errs, fmt.Errorf("monitor[%d]: id is required", i))
			continue
		}
		if _, ok := out[mt.ID]; ok {
			errs = append(errs, fmt.Errorf("duplicate monitor id %q", mt.ID))
			continue
		}
		out[mt.ID] = mt.Subset
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// decodeGlobalBudget reads the XDG-global config file and applies ONLY its
// [pool].budget over c (budget-only scope: self_login, worktree_dir, [[role]] and
// every other key are intentionally ignored — roles/scalars stay repo-local +
// built-in per spec C). A present-but-malformed file is a hard error, matching
// decodeRoleSet. Caller stats the path first; this assumes the file exists.
func (r *Registry) decodeGlobalBudget(path string, c *Config) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var shape fileShape
	if _, err := toml.Decode(string(body), &shape); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	overlayConfigBudget(c, shape.Pool.Budget)
	return nil
}

func (r *Registry) buildRole(md toml.MetaData, rt roleTOML, configDir string, c Config) (roles.Role, error) {
	if rt.Name == "" {
		return roles.Role{}, fmt.Errorf("name is required")
	}
	// Charset/path-safety hardening (this bead, pg2-ymb3v), independent of
	// HandlerCommandDir: cmd/pg-router/run.go's handlerCommandFor joins
	// role.Name directly into a filesystem path
	// (filepath.Join(cfg.HandlerCommandDir, role.Name+".json")), so a name
	// containing "/" breaks that join (a role subdirectory that was never
	// declared) and one containing ".." can path-traverse out of
	// HandlerCommandDir entirely. Role names are TOML-authored only (no
	// wire-level role field carries one), so this is a config footgun to
	// reject at decode time, not an attacker-exploitable input.
	if strings.Contains(rt.Name, "/") || strings.Contains(rt.Name, "..") {
		return roles.Role{}, fmt.Errorf("name %q must not contain '/' or '..' (joined into a filesystem path under PG_ROUTER_HANDLER_COMMAND_DIR)", rt.Name)
	}
	if len(rt.Binds) == 0 {
		return roles.Role{}, fmt.Errorf("binds is required (the event type(s) this role consumes)")
	}
	enabled := true
	if rt.Enabled != nil {
		enabled = *rt.Enabled
	}
	retryBackoff, err := buildBackoffPolicy(c.RetryBackoff, rt.Retry)
	if err != nil {
		return roles.Role{}, fmt.Errorf("retry: %w", err)
	}
	return roles.Role{Name: rt.Name, Enabled: enabled, Binds: rt.Binds, RetryBackoff: retryBackoff}, nil
}

// buildQuery decodes one [[query]] into a concrete query.Query, installing its
// [[query]]-level Meta (emits + trigger).
func (r *Registry) buildQuery(md toml.MetaData, qt queryTOML, c Config) (query.Query, error) {
	if len(qt.Emits) == 0 {
		return nil, fmt.Errorf("emits is required (the event type(s) this query produces)")
	}
	prims := map[string]toml.Primitive{
		"command": qt.Command,
	}
	prim, ok := prims[qt.Type]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q", qt.Type)
	}
	trig, err := buildTrigger(qt.Trigger, c.PollInterval)
	if err != nil {
		return nil, err
	}
	fb, err := buildFailureBackoff(query.FailureBackoff{Policy: c.PullFailureBackoff, Retries: c.PullFailureRetries}, qt.FailureBackoff)
	if err != nil {
		return nil, fmt.Errorf("failure_backoff: %w", err)
	}
	return r.queries.Decode(qt.Type, query.Meta{EmitTypes: qt.Emits, Trig: trig, FB: fb}, md, prim)
}

// buildBackoffPolicy overlays a [*.retry]-shaped TOML table onto base — the
// retry-cadence SHAPE (INV-FAIL-2 / INV-FAIL-3, pg2-0c8yz): a short initial
// wait, growing by factor on each consecutive failure, capped at max. An
// absent table returns base unchanged (inherit the pool default verbatim).
func buildBackoffPolicy(base backoff.Policy, t *backoffTOML) (backoff.Policy, error) {
	if t == nil {
		return base, nil
	}
	if t.Initial != nil {
		if t.Initial.D <= 0 {
			return backoff.Policy{}, fmt.Errorf("initial must be > 0")
		}
		base.Initial = t.Initial.D
	}
	if t.Factor != 0 {
		if t.Factor <= 1 {
			return backoff.Policy{}, fmt.Errorf("factor must be > 1 (it must GROW the wait on each consecutive failure)")
		}
		base.Factor = t.Factor
	}
	if t.Max != nil {
		if t.Max.D <= 0 {
			return backoff.Policy{}, fmt.Errorf("max must be > 0")
		}
		base.Max = t.Max.D
	}
	return base, nil
}

// buildFailureBackoff overlays a [*.failure_backoff]-shaped TOML table onto
// base — the pull-source failure backoff (INV-FAIL-3): the same SHAPE as
// buildBackoffPolicy plus retries, the bound on how many further attempts
// discover.Produce makes within one pass before giving up. An absent table
// returns base unchanged (inherit the pool default verbatim, Retries: 0 unless
// the pool default itself opted in).
func buildFailureBackoff(base query.FailureBackoff, t *failureBackoffTOML) (query.FailureBackoff, error) {
	fb := base
	if t == nil {
		return fb, nil
	}
	policy, err := buildBackoffPolicy(fb.Policy, &backoffTOML{Initial: t.Initial, Factor: t.Factor, Max: t.Max})
	if err != nil {
		return query.FailureBackoff{}, err
	}
	fb.Policy = policy
	if t.Retries != nil {
		if *t.Retries < 0 {
			return query.FailureBackoff{}, fmt.Errorf("retries must be >= 0")
		}
		fb.Retries = *t.Retries
	}
	return fb, nil
}

// buildTrigger maps a [query.trigger] table to the concrete Trigger strategy
// (Q1). An absent table (or kind "" / "period") is PeriodTrigger — a period
// query with no explicit `every` inherits the pool PollInterval, reproducing
// today's once-per-pass pull.
func buildTrigger(t *triggerTOML, pollInterval time.Duration) (query.Trigger, error) {
	if t == nil || t.Kind == "" || t.Kind == "period" {
		every := pollInterval
		if t != nil && t.Every != nil {
			every = t.Every.D
		}
		return query.PeriodTrigger{Every: every}, nil
	}
	switch t.Kind {
	case "threshold":
		if t.Count <= 0 {
			return nil, fmt.Errorf("threshold trigger: count must be > 0")
		}
		if len(t.Binds) == 0 {
			return nil, fmt.Errorf("threshold trigger: binds is required (the upstream event type(s) to count)")
		}
		return query.ThresholdTrigger{Binds: t.Binds, Count: t.Count}, nil
	case "manual":
		return query.ManualTrigger{}, nil
	default:
		return nil, fmt.Errorf("unknown trigger kind %q (known: period, threshold, manual)", t.Kind)
	}
}

// overlayConfigBudget applies the [pool].budget over the Config's budget scalars.
func overlayConfigBudget(c *Config, t *budgetTOML) {
	if t == nil {
		return
	}
	if t.Tokens != nil {
		c.BudgetTokens = *t.Tokens
	}
	if t.Cost != nil {
		c.BudgetCost = *t.Cost
	}
	if t.Time != nil {
		c.BudgetTime = t.Time.D
	}
}
