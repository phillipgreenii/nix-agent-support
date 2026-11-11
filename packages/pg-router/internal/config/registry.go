package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
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

// byteSize is a TOML byte-count value: an integer ("max_log_bytes = 67108864") or a
// string with a unit ("max_log_bytes = \"64MiB\""), parsed by ParseBytes - the same
// grammar PG_ROUTER_MAX_LOG_BYTES takes (bead pg2-maxn1, which closes the gap
// pg2-5d3ui left: TOML used to accept an integer only). Anything else (a float, a
// boolean, an unparseable string) is an error naming the value, never a silent
// default.
type byteSize int64

// UnmarshalTOML implements toml.Unmarshaler.
func (b *byteSize) UnmarshalTOML(data any) error {
	switch v := data.(type) {
	case int64:
		*b = byteSize(v)
		return nil
	case string:
		n, err := ParseBytes(v)
		if err != nil {
			return err
		}
		*b = byteSize(n)
		return nil
	}
	return fmt.Errorf("want an integer byte count or a string with a unit such as \"64MiB\", got %v (%T)", data, data)
}

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
	// Levels and Resources are the level settings and the resource registry
	// (resources.go); Requires on a role is compiled by requires.go.
	Levels    *levelsTOML    `toml:"levels"`
	Resources []resourceTOML `toml:"resource"`
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
	// EarlyEvictionTypes opts each named event TYPE in to early eviction
	// (pg2-u0p2d, DEC-EVENT-2): the queue evicts an event of that type once
	// every bound handler has accepted it, instead of retaining it until
	// expiresAt. Absent/empty: every type keeps full retention (unchanged).
	EarlyEvictionTypes []string `toml:"early_eviction_types"`
	// CompactThresholdBytes is the queue.jsonl size that triggers a background
	// compaction of the log (bead pg2-8e0m6). A pointer so an explicit 0
	// (disable runtime compaction) is distinguishable from absent (keep the
	// default / env value).
	CompactThresholdBytes *byteSize `toml:"compact_threshold_bytes"`
	// MaxLogBytes is the HARD size limit of the queue.jsonl write-ahead log (bead
	// pg2-5d3ui). A pointer so an explicit 0 is distinguishable from absent (and
	// rejected, rather than silently meaning "default").
	MaxLogBytes *byteSize `toml:"max_log_bytes"`
	// OperatorPausedPath / CICDDownPath / DiskSpaceLowPath are RETIRED (bead
	// pg2-h63eu): they named the file-backed gates the Gate Registry replaced.
	// They are still decoded so a config that carries them keeps loading;
	// decodeRoleSet logs a deprecation warning and otherwise ignores them.
	OperatorPausedPath string `toml:"operator_paused_path"`
	CICDDownPath       string `toml:"cicd_down_path"`
	DiskSpaceLowPath   string `toml:"disk_space_low_path"`
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
	// Description is an optional, operator-authored free-text note
	// (pg2-ec754), threaded verbatim onto roles.Role.Description and
	// surfaced in the TUI drill-down details. Absent: empty default.
	Description string `toml:"description"`
	// Requires is the role's requires clauses ("<resource> <op> <LEVEL>"),
	// compiled against the [[resource]] registry (requires.go).
	Requires []string `toml:"requires"`
	// MaxDispatchRetries opts this role in to a bounded re-run of a dispatch
	// that failed with a transient error (killed / deadline / unavailable;
	// bead pg2-yu5y2). Absent or 0: no re-run (the default). Must be within
	// 0..roles.MaxDispatchRetriesCap.
	MaxDispatchRetries *int `toml:"max_dispatch_retries"`
	// Handler is the role's own adapter command (an absolute path; docket
	// pg2-12gr4.10, the handler split). A role that sets it is on the NEW path.
	// A pointer so an explicit empty string is rejected rather than read as unset.
	Handler *string `toml:"handler"`
	// Config is the opaque [role.config] table handed to the adapter as
	// role.config on every request; core never interprets it. Decoded as any
	// and type-checked in applyHandlerKeys: the TOML library silently leaves a
	// map field nil when the key holds a non-table value, which would hide the
	// type error.
	Config any `toml:"config"`
	// SurvivesShutdown is the generic survives_shutdown key (default false),
	// read directly from the TOML for a role with Handler.
	SurvivesShutdown *bool `toml:"survives_shutdown"`
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
	// ExpectedInterval is an optional, explicit override for staleness
	// display purposes (this task): unlike Trigger.Every, it carries no
	// scheduling meaning — it exists so a future non-period-triggered or
	// push-mode source can still declare its own expected cadence. An
	// explicit value always wins over a period trigger's own resolved
	// Every.
	ExpectedInterval *duration `toml:"expected_interval"`
	// Description is an optional, operator-authored free-text note
	// (pg2-ec754), threaded verbatim onto query.Source.Description and
	// surfaced in the TUI drill-down details. Absent: empty default.
	Description string `toml:"description"`
	// LevelSource names the sensor this query answers for (it is a level-source
	// query: it has no consumer and is not gated). Empty: an ordinary query.
	LevelSource string `toml:"level_source"`
	// Deadline is the per-pull timeout of this query (default
	// query.DefaultDeadline, 20s).
	Deadline *duration `toml:"deadline"`
	// StrictBaseEnv switches a command query's process onto the minimal base
	// environment (default off). A pointer so a timer query that sets it can be
	// told apart from one that does not.
	StrictBaseEnv *bool `toml:"strict_base_env"`
	// EnvAllow lists the environment variable names a command query's process
	// receives besides the minimal base environment (for example SSH_AUTH_SOCK
	// for a git-credential sensor). Absent: the process inherits the router's
	// environment, unless strict_base_env is on.
	EnvAllow []string `toml:"env_allow"`
	// Metadata is a timer query's static metadata, copied unchanged into every
	// tick's item. Decoded as any so a non-string value is reported by our own
	// error, not the TOML decoder's.
	Metadata map[string]any `toml:"metadata"`
}

// triggerTOML is a query's firing strategy (Q1). kind selects the concrete
// Strategy: "period" (default), "threshold", or "manual".
type triggerTOML struct {
	Kind  string    `toml:"kind"`
	Every *duration `toml:"every"` // period
	Count int       `toml:"count"` // threshold
	Binds []string  `toml:"binds"` // threshold: the upstream types to count

	// Calendar-trigger keys (consumed by the calendar kind).
	At        string    `toml:"at"`
	Days      []string  `toml:"days"`
	TZ        *string   `toml:"tz"`
	OnMissed  string    `toml:"on_missed"`
	ExpiresIn *duration `toml:"expires_in"`
}

// calendarOnlyTriggerKeys are the trigger keys valid only for kind = "calendar".
var calendarOnlyTriggerKeys = []string{"at", "days", "tz", "on_missed", "expires_in"}

// reservedMetadataKeys are the keys the core sets itself on a timer tick.
var reservedMetadataKeys = []string{"scheduled_for", "on_missed"}

// maxTimerMetadataKeys bounds a timer query's static metadata.
const maxTimerMetadataKeys = 16

// knownTriggerKeys lists every key triggerTOML consumes, from its toml tags.
func knownTriggerKeys() []string {
	t := reflect.TypeOf(triggerTOML{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if tag := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]; tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	sort.Strings(keys)
	return keys
}

// rawTriggerKeys returns, per [[query]] in file order, the keys of its trigger
// table (nil when it has none), like rawQueryKeys but one level down. Used to
// reject a misspelled key inside [query.trigger] and to see which calendar-only
// keys were written.
func rawTriggerKeys(body string) [][]string {
	var raw struct {
		Queries []map[string]any `toml:"query"`
	}
	if _, err := toml.Decode(body, &raw); err != nil {
		return nil
	}
	out := make([][]string, len(raw.Queries))
	for i, m := range raw.Queries {
		tm, ok := m["trigger"].(map[string]any)
		if !ok {
			continue
		}
		for k := range tm {
			out[i] = append(out[i], k)
		}
		sort.Strings(out[i])
	}
	return out
}

// checkTriggerKeys rejects unknown keys in a query's trigger table and a
// calendar-only key on a trigger whose kind is not calendar. When every is also
// set, the calendar packet's mutual-exclusion message covers it instead.
func checkTriggerKeys(keys []string, t *triggerTOML) error {
	known := map[string]bool{}
	for _, k := range knownTriggerKeys() {
		known[k] = true
	}
	var unknown, calendar []string
	for _, k := range keys {
		switch {
		case !known[k]:
			unknown = append(unknown, k)
		case slices.Contains(calendarOnlyTriggerKeys, k):
			calendar = append(calendar, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("trigger: unknown key(s) %s (a misspelled key is never silently ignored; known keys: %s)", strings.Join(unknown, ", "), strings.Join(knownTriggerKeys(), ", "))
	}
	if len(calendar) > 0 && (t == nil || (t.Kind != "calendar" && t.Every == nil)) {
		return fmt.Errorf("trigger: \"at\", \"days\", \"tz\", \"on_missed\" and \"expires_in\" are only valid for kind = \"calendar\" (found: %s)", strings.Join(calendar, ", "))
	}
	return nil
}

// tomlTypeName names a decoded TOML value's type for a metadata error.
func tomlTypeName(v any) string {
	switch v.(type) {
	case int64:
		return "integer"
	case float64:
		return "float"
	case bool:
		return "bool"
	case []any, []map[string]any:
		return "array"
	case map[string]any:
		return "table"
	case time.Time:
		return "datetime"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// buildTimerMetadata validates a query's static metadata and returns it as
// strings. An empty or absent map is nil (metadata = {} equals absent).
func buildTimerMetadata(qt queryTOML) (map[string]string, error) {
	if len(qt.Metadata) == 0 {
		return nil, nil
	}
	if qt.Type != "timer" {
		return nil, fmt.Errorf("metadata is only supported on type = \"timer\" queries; this query is type %q", qt.Type)
	}
	keys := make([]string, 0, len(qt.Metadata))
	for k := range qt.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if slices.Contains(reservedMetadataKeys, k) {
			return nil, fmt.Errorf("metadata key %q is reserved (reserved: %s)", k, strings.Join(reservedMetadataKeys, ", "))
		}
		sv, ok := qt.Metadata[k].(string)
		if !ok {
			return nil, fmt.Errorf("metadata.%s must be a string, got %s", k, tomlTypeName(qt.Metadata[k]))
		}
		out[k] = sv
	}
	if len(keys) > maxTimerMetadataKeys {
		return nil, fmt.Errorf("metadata has %d keys; at most %d are allowed", len(keys), maxTimerMetadataKeys)
	}
	return out, nil
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
	if err := checkUndecodedKeys(md, "levels", "resource"); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	lc, err := buildLevelsConfig(shape.Levels)
	if err != nil {
		return nil, fmt.Errorf("levels: %w", err)
	}
	c.Levels = lc
	resources, rerrs := buildResources(shape.Resources, lc)
	if len(rerrs) > 0 {
		return nil, errors.Join(rerrs...)
	}
	c.Resources = resources
	if shape.Pool.SelfLogin != "" {
		c.SelfLogin = shape.Pool.SelfLogin
	}
	// [pool].worktree_dir overlays after the env overlay in Load(), so config
	// (repo) takes precedence over PG_ROUTER_WORKTREE_DIR (env, global). An absent
	// key leaves the env/default value intact.
	if shape.Pool.WorktreeDir != "" {
		c.WorktreeDir = shape.Pool.WorktreeDir
	}
	for key, val := range map[string]string{
		"operator_paused_path": shape.Pool.OperatorPausedPath,
		"cicd_down_path":       shape.Pool.CICDDownPath,
		"disk_space_low_path":  shape.Pool.DiskSpaceLowPath,
	} {
		if val != "" {
			slog.Warn("config: [pool]."+key+" is retired and ignored; gates are now TYPE-keyed records in the event log (see MIGRATION.md, \"Gates: file-backed gates replaced by the Gate Registry\")", "path", path)
		}
	}
	overlayConfigBudget(c, shape.Pool.Budget)
	// serialize_types (INV-CONC-1, pg2-cl9jz): a present, non-empty list REPLACES
	// the default (empty — marks nothing); an absent/empty key leaves c's
	// existing value (Default()'s nil) untouched. There is no per-role/per-query
	// overlay for this one — see DEC-CONC-1's "not decided here".
	if len(shape.Pool.SerializeTypes) > 0 {
		c.SerializeTypes = shape.Pool.SerializeTypes
	}
	// early_eviction_types (pg2-u0p2d, DEC-EVENT-2): same present-replaces,
	// absent-leaves-default shape as serialize_types above.
	if len(shape.Pool.EarlyEvictionTypes) > 0 {
		c.EarlyEvictionTypes = shape.Pool.EarlyEvictionTypes
	}
	if v := shape.Pool.CompactThresholdBytes; v != nil {
		if *v < 0 {
			return nil, fmt.Errorf("pool.compact_threshold_bytes must be >= 0 (0 disables runtime compaction), got %d", *v)
		}
		c.CompactThresholdBytes = int64(*v)
	}
	if v := shape.Pool.MaxLogBytes; v != nil {
		if *v <= 0 {
			return nil, fmt.Errorf("pool.max_log_bytes must be > 0, got %d", *v)
		}
		c.MaxLogBytes = int64(*v)
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
	queries, overrides, qerrs := r.buildQueries(md, shape.Queries, rawQueryKeys(string(body)), rawTriggerKeys(string(body)), *c)
	errs = append(errs, qerrs...)
	requirements := map[string]Compiled{}
	for _, role := range out {
		comp, cerrs := CompileRequires(c.Resources, fmt.Sprintf("role %q", role.Name), role.Requires)
		errs = append(errs, cerrs...)
		requirements[role.Name] = comp
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	c.Requirements = requirements
	c.Queries = queries
	c.ExpectedIntervalOverrides = overrides
	return out, nil
}

// buildQueries decodes every [[query]] into a named producer (query.Source),
// installing its emits + trigger, and collects each query's own explicit
// expected_interval override (this task) into a name-keyed map. Duplicate
// query names are rejected.
func (r *Registry) buildQueries(md toml.MetaData, qts []queryTOML, rawKeys, rawTrigKeys [][]string, c Config) (query.SourceSet, map[string]int64, []error) {
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
		if i < len(rawKeys) {
			if unknown := unknownQueryKeys(rawKeys[i]); len(unknown) > 0 {
				errs = append(errs, fmt.Errorf("query[%d] %q: unknown key(s) %s (a misspelled key is never silently ignored; known keys: %s)", i, qt.Name, strings.Join(unknown, ", "), strings.Join(knownQueryKeys(), ", ")))
				continue
			}
		}
		if i < len(rawTrigKeys) && len(rawTrigKeys[i]) > 0 {
			if err := checkTriggerKeys(rawTrigKeys[i], qt.Trigger); err != nil {
				errs = append(errs, fmt.Errorf("query[%d] %q: %w", i, qt.Name, err))
				continue
			}
		}
		q, err := r.buildQuery(md, qt, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("query[%d] %q: %w", i, qt.Name, err))
			continue
		}
		deadline := query.DefaultDeadline
		if qt.Deadline != nil {
			if qt.Deadline.D <= 0 {
				errs = append(errs, fmt.Errorf("query[%d] %q: deadline must be > 0, got %s", i, qt.Name, qt.Deadline.D))
				continue
			}
			deadline = qt.Deadline.D
		}
		out = append(out, query.Source{Name: qt.Name, Query: q, Description: qt.Description, LevelSource: qt.LevelSource, Deadline: deadline})
		if qt.ExpectedInterval != nil {
			overrides[qt.Name] = qt.ExpectedInterval.D.Milliseconds()
		}
	}
	return out, overrides, errs
}

// knownQueryKeys lists every key queryTOML consumes, taken from its toml tags
// so a new field is known automatically.
func knownQueryKeys() []string {
	t := reflect.TypeOf(queryTOML{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if tag := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]; tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	sort.Strings(keys)
	return keys
}

// unknownQueryKeys returns the keys of one [[query]] table that queryTOML does
// not consume, sorted. Core's TOML decode is lenient elsewhere, but a [[query]]
// key is strict because a misspelled strict_base_env
// (strictBaseEnv) would silently run a producer on the ambient environment.
func unknownQueryKeys(keys []string) []string {
	known := map[string]bool{}
	for _, k := range knownQueryKeys() {
		known[k] = true
	}
	var unknown []string
	for _, k := range keys {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// rawQueryKeys returns, per [[query]] in file order, the direct keys of its
// table. It is a second, untyped decode of the same body, aligned by index with
// fileShape.Queries; toml.MetaData.Undecoded() cannot be used for this because
// its keys carry no array-of-tables index, so it cannot name the offending
// query. A body that does not decode here already failed the typed decode.
func rawQueryKeys(body string) [][]string {
	var raw struct {
		Queries []map[string]any `toml:"query"`
	}
	if _, err := toml.Decode(body, &raw); err != nil {
		return nil
	}
	out := make([][]string, len(raw.Queries))
	for i, m := range raw.Queries {
		for k := range m {
			out[i] = append(out[i], k)
		}
	}
	return out
}

// calendarNow is the clock the calendar expected-interval scan starts from: the
// 14-day window of slots is measured from it, and tests replace it so the result
// is deterministic. The zone used is the trigger's own.
var calendarNow = time.Now

// ExpectedIntervalMsFor returns, in milliseconds, src's expected tick
// cadence: an explicit `expected_interval` config override (overrides,
// keyed by src.Name) always wins; otherwise a `kind: "period"` query's own
// resolved trigger interval; otherwise, for a calendar query, the largest gap
// between consecutive slots over the next 14 days (a daily source 86400 s, a
// weekly one 604800 s, so the persistent-failure alert is not tripped by the
// 10 s poll interval standing in for a missing interval);
// otherwise 0 (unknown: threshold/manual triggers, or a period query with
// neither).
func ExpectedIntervalMsFor(src query.Source, overrides map[string]int64) int64 {
	if ms, ok := overrides[src.Name]; ok {
		return ms
	}
	if pt, ok := src.Query.Trigger().(query.PeriodTrigger); ok && pt.Every > 0 {
		return pt.Every.Milliseconds()
	}
	if ct, ok := query.Calendar(src.Query.Trigger()); ok {
		return ct.MaxGap(calendarNow()).Milliseconds()
	}
	return 0
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
	// Charset/path-safety hardening (pg2-ymb3v): a role name is used as a
	// filesystem path component (the per-role log and state names derive from
	// it), so a name containing "/" names a subdirectory that was never declared
	// and one containing ".." can path-traverse. Role names are TOML-authored
	// only (no wire-level role field carries one), so this is a config footgun
	// to reject at decode time, not an attacker-exploitable input.
	if strings.Contains(rt.Name, "/") || strings.Contains(rt.Name, "..") {
		return roles.Role{}, fmt.Errorf("name %q must not contain '/' or '..' (used as a filesystem path component)", rt.Name)
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
	maxRetries := 0
	if rt.MaxDispatchRetries != nil {
		maxRetries = *rt.MaxDispatchRetries
	}
	if maxRetries < 0 || maxRetries > roles.MaxDispatchRetriesCap {
		return roles.Role{}, fmt.Errorf("max_dispatch_retries = %d: must be between 0 and %d (a hard cap so a transient-failure storm cannot amplify load)", maxRetries, roles.MaxDispatchRetriesCap)
	}
	role := roles.Role{Name: rt.Name, Enabled: enabled, Binds: rt.Binds, RetryBackoff: retryBackoff, MaxDispatchRetries: maxRetries, Description: rt.Description, Requires: rt.Requires}
	if err := applyHandlerKeys(&role, rt); err != nil {
		return roles.Role{}, err
	}
	return role, nil
}

// applyHandlerKeys decodes the per-role keys handler, [role.config] and
// survives_shutdown strictly: a wrong type fails the decode (the typed TOML
// decode already did that) and handler is REQUIRED, a non-empty absolute path.
// A [[role]] without handler is a load error naming the role (the caller
// prefixes it): a role is invoked only through its own handler, and core has no
// shared fallback command to run it with. Every other unknown [[role]] key is
// still ignored, exactly as before.
func applyHandlerKeys(role *roles.Role, rt roleTOML) error {
	if rt.Handler == nil {
		return fmt.Errorf("handler is required: a role is invoked only through its own handler (set handler to the adapter's absolute path, e.g. pg-router-handler-ccpool or pg-router-handler-command)")
	}
	if !filepath.IsAbs(*rt.Handler) {
		return fmt.Errorf("handler %q must be an absolute path", *rt.Handler)
	}
	role.Handler = *rt.Handler
	role.Config = map[string]any{}
	if rt.Config != nil {
		cfg, ok := rt.Config.(map[string]any)
		if !ok {
			return fmt.Errorf("config must be a table ([role.config]), got %T", rt.Config)
		}
		role.Config = cfg
	}
	role.SurvivesShutdown = rt.SurvivesShutdown != nil && *rt.SurvivesShutdown
	return nil
}

// envNameRE is the shape of an environment variable name an env_allow list may
// carry.
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// buildQuery decodes one [[query]] into a concrete query.Query, installing its
// [[query]]-level Meta (emits + trigger).
func (r *Registry) buildQuery(md toml.MetaData, qt queryTOML, c Config) (query.Query, error) {
	if len(qt.Emits) == 0 && qt.LevelSource == "" {
		return nil, fmt.Errorf("emits is required (the event type(s) this query produces)")
	}
	prims := map[string]toml.Primitive{
		"command": qt.Command,
		// timer has no sub-table: a zero Primitive is never decoded.
		"timer": {},
	}
	prim, ok := prims[qt.Type]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q", qt.Type)
	}
	trig, err := buildTrigger(qt.Trigger, c.PollInterval, qt.Type)
	if err != nil {
		return nil, err
	}
	fb, err := buildFailureBackoff(query.FailureBackoff{Policy: c.PullFailureBackoff, Retries: c.PullFailureRetries}, qt.FailureBackoff)
	if err != nil {
		return nil, fmt.Errorf("failure_backoff: %w", err)
	}
	if qt.Type != "command" && len(qt.EnvAllow) > 0 {
		return nil, fmt.Errorf("env_allow applies only to a command query; a %q query has no command to run", qt.Type)
	}
	for _, name := range qt.EnvAllow {
		if !envNameRE.MatchString(name) {
			return nil, fmt.Errorf("env_allow: %q is not an environment variable name", name)
		}
	}
	if qt.LevelSource != "" && qt.Type != "command" {
		return nil, fmt.Errorf("level_source applies only to a command query; a %q query cannot answer with level records", qt.Type)
	}
	if qt.Type != "command" && qt.StrictBaseEnv != nil {
		return nil, fmt.Errorf("strict_base_env applies only to a command query; a %q query has no command to run", qt.Type)
	}
	tmeta, err := buildTimerMetadata(qt)
	if err != nil {
		return nil, err
	}
	q, err := r.queries.Decode(qt.Type, query.Meta{EmitTypes: qt.Emits, Trig: trig, FB: fb}, md, prim)
	if err != nil {
		return nil, err
	}
	if tq, ok := q.(query.TimerQuery); ok {
		tq.Metadata = tmeta
		return tq, nil
	}
	if cq, ok := q.(query.CommandQuery); ok {
		cq.StrictBaseEnv = qt.StrictBaseEnv != nil && *qt.StrictBaseEnv
		cq.EnvAllow = append([]string(nil), qt.EnvAllow...)
		return cq, nil
	}
	return q, nil
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
func buildTrigger(t *triggerTOML, pollInterval time.Duration, queryType string) (query.Trigger, error) {
	if t != nil && t.Every != nil {
		var errs []error
		if t.At != "" {
			errs = append(errs, fmt.Errorf("trigger: \"every\" and \"at\" are mutually exclusive (\"every\" belongs to kind = \"period\", \"at\" to kind = \"calendar\")"))
		}
		if t.Days != nil {
			errs = append(errs, fmt.Errorf("trigger: \"every\" and \"days\" are mutually exclusive (\"every\" belongs to kind = \"period\", \"days\" to kind = \"calendar\")"))
		}
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
	}
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
	case "calendar":
		return buildCalendarTrigger(t, queryType)
	default:
		return nil, fmt.Errorf("unknown trigger kind %q (known: period, threshold, manual, calendar)", t.Kind)
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

// checkUndecodedKeys rejects a key under one of the named top-level tables
// that no field decodes (a misspelled key is never silently ignored).
func checkUndecodedKeys(md toml.MetaData, tables ...string) error {
	var bad []string
	for _, k := range md.Undecoded() {
		if len(k) > 0 && slices.Contains(tables, k[0]) {
			bad = append(bad, k.String())
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("unknown key(s) %s", strings.Join(bad, ", "))
	}
	return nil
}
