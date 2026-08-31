package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
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
	Pool    poolTOML    `toml:"pool"`
	Roles   []roleTOML  `toml:"role"`
	Queries []queryTOML `toml:"query"`
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
	// `packages/pr-pool/docs/decisions · DEC-CONC-1`): the queue offers at most
	// one event of a marked type at a time, across every bound handler, until
	// it is released. Absent/empty: marks nothing (unchanged from today).
	SerializeTypes []string `toml:"serialize_types"`
	// QuotaPausedPath / CICDDownPath override the two INV-LIFE-2 gate file
	// paths `pause`/`resume` act on. Absent: PR_POOL_QUOTA_PAUSED /
	// PR_POOL_CICD_DOWN env, then <LogDir>/gates/{quota-paused,cicd-down}
	// (config.Load()'s post-repo-TOML fill; config.GatePaths() resolves the
	// identical precedence without Load()).
	QuotaPausedPath string `toml:"quota_paused_path"`
	CICDDownPath    string `toml:"cicd_down_path"`
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
	Name    string         `toml:"name"`
	Type    string         `toml:"type"`
	Enabled *bool          `toml:"enabled"` // pointer: absent => default true
	Binds   []string       `toml:"binds"`   // event types this role consumes (Observer)
	CCPool  toml.Primitive `toml:"ccpool"`  // decoded by buildCCPool iff type==ccpool
	Command toml.Primitive `toml:"command"` // decoded by buildCommand iff type==command
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

// ccpoolTOML is decoded from the [role.ccpool] primitive. The enum fields validate
// at decode (UnmarshalText), so an invalid value fails with TOML location context.
type ccpoolTOML struct {
	Actor           string                   `toml:"actor"`
	SkillMD         string                   `toml:"skill_md"`
	Completion      roles.Completion         `toml:"completion"`
	OnFailure       roles.FailureAction      `toml:"on_failure"`
	OnDispatchFail  roles.DispatchFailAction `toml:"on_dispatch_fail"`
	AuthorshipGuard bool                     `toml:"authorship_guard"`
	Prompt          string                   `toml:"prompt"`
	PromptFile      string                   `toml:"prompt_file"`
	Budget          *budgetTOML              `toml:"budget"`
	Isolation       *isolationTOML           `toml:"isolation"`
}

// isolationTOML is decoded from the optional [role.ccpool.isolation] table. An
// absent table (nil) leaves roles.IsolationConfig at its zero value, which
// buildCCPool below resolves to "worktree" — today's only behavior.
type isolationTOML struct {
	Type string `toml:"type"`
	Path string `toml:"path"`
}

type commandTOML struct {
	Argv []string `toml:"argv"`
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
	// (repo) takes precedence over PR_POOL_WORKTREE_DIR (env, global). An absent
	// key leaves the env/default value intact.
	if shape.Pool.WorktreeDir != "" {
		c.WorktreeDir = shape.Pool.WorktreeDir
	}
	// [pool].quota_paused_path / cicd_down_path overlay after the env overlay in
	// Load() the same way worktree_dir does — config (repo) wins over
	// PR_POOL_QUOTA_PAUSED / PR_POOL_CICD_DOWN (env), which already won over
	// Default()'s "". An absent key leaves the env/default value intact; Load()
	// fills either still-empty field from <LogDir>/gates/... AFTER this returns.
	if shape.Pool.QuotaPausedPath != "" {
		c.QuotaPaused = shape.Pool.QuotaPausedPath
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
	role := roles.Role{Name: rt.Name, Type: rt.Type, Enabled: enabled, Binds: rt.Binds, RetryBackoff: retryBackoff}
	switch rt.Type {
	case "ccpool":
		cc, err := buildCCPool(md, rt.CCPool, configDir, c)
		if err != nil {
			return roles.Role{}, err
		}
		role.CCPool = cc
	case "command":
		cmd, err := buildCommand(md, rt.Command)
		if err != nil {
			return roles.Role{}, err
		}
		role.Command = cmd
	default:
		return roles.Role{}, fmt.Errorf("unknown role type %q (known: ccpool, command)", rt.Type)
	}
	return role, nil
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

func buildCCPool(md toml.MetaData, prim toml.Primitive, configDir string, c Config) (*roles.CCPoolConfig, error) {
	var ct ccpoolTOML
	if err := md.PrimitiveDecode(prim, &ct); err != nil {
		return nil, err
	}
	if (ct.Prompt == "") == (ct.PromptFile == "") {
		return nil, fmt.Errorf("ccpool role: exactly one of prompt / prompt_file is required")
	}
	if ct.Completion == "" || ct.OnFailure == "" || ct.OnDispatchFail == "" {
		return nil, fmt.Errorf("ccpool role: completion, on_failure, and on_dispatch_fail are required")
	}
	// actor is required: a ccpool role dispatches under BEADS_ACTOR, and an empty
	// actor (e.g. from a typo'd `actorr =` key, which BurntSushi silently ignores)
	// would create beads with no attribution and break the created-marker diff.
	if ct.Actor == "" {
		return nil, fmt.Errorf("ccpool role: actor is required")
	}
	body := ct.Prompt
	if ct.PromptFile != "" {
		b, err := os.ReadFile(filepath.Join(configDir, ct.PromptFile))
		if err != nil {
			return nil, fmt.Errorf("ccpool role: prompt_file: %w", err)
		}
		body = string(b)
	}
	tmpl, err := prompt.Parse("role-prompt", body)
	if err != nil {
		return nil, fmt.Errorf("ccpool role: prompt template: %w", err)
	}
	b := c.WorkerBudget() // pool default budget; per-role budget overlays it
	overlayBudget(&b, ct.Budget)
	isolation, err := buildIsolation(ct.Isolation)
	if err != nil {
		return nil, fmt.Errorf("ccpool role: isolation: %w", err)
	}
	return &roles.CCPoolConfig{
		Actor:           ct.Actor,
		SkillMD:         ct.SkillMD,
		Completion:      ct.Completion,
		OnFailure:       ct.OnFailure,
		OnDispatchFail:  ct.OnDispatchFail,
		AuthorshipGuard: ct.AuthorshipGuard,
		PromptBody:      body,
		Prompt:          tmpl,
		Budget:          b,
		Isolation:       isolation,
	}, nil
}

// buildIsolation validates an optional [role.ccpool.isolation] table. A nil
// table (the key omitted entirely) resolves to the zero IsolationConfig, which
// the executor treats as "worktree" — so an existing config is unaffected.
func buildIsolation(t *isolationTOML) (roles.IsolationConfig, error) {
	if t == nil {
		return roles.IsolationConfig{}, nil
	}
	switch t.Type {
	case "", "worktree", "none", "workforest":
		if t.Path != "" {
			return roles.IsolationConfig{}, fmt.Errorf("path is only valid for type %q, not %q", "path", t.Type)
		}
	case "path":
		if t.Path == "" {
			return roles.IsolationConfig{}, fmt.Errorf("type %q requires path", "path")
		}
	default:
		return roles.IsolationConfig{}, fmt.Errorf("unknown type %q (known: worktree, none, path, workforest)", t.Type)
	}
	return roles.IsolationConfig{Type: t.Type, Path: t.Path}, nil
}

func buildCommand(md toml.MetaData, prim toml.Primitive) (*roles.CommandConfig, error) {
	var ct commandTOML
	if err := md.PrimitiveDecode(prim, &ct); err != nil {
		return nil, err
	}
	if len(ct.Argv) == 0 {
		return nil, fmt.Errorf("command role: argv is required")
	}
	return &roles.CommandConfig{Argv: ct.Argv}, nil
}

// overlayBudget applies a per-role budget over a base (pool default), field by field.
func overlayBudget(b *budget.Budget, t *budgetTOML) {
	if t == nil {
		return
	}
	if t.Tokens != nil {
		b.Tokens = budget.Limit(*t.Tokens)
	}
	if t.Cost != nil {
		b.Cost = budget.Limit(*t.Cost)
	}
	if t.Time != nil {
		b.Time = t.Time.D
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
