// Package config holds pr-pool's runtime configuration. Pool scalars layer
// Default() -> PR_POOL_* env -> [pool] TOML (the config file wins for the keys it
// sets: self_login, worktree_dir, budget). Roles come from the [[role]] array in
// <RepoRoot>/.pr-pool/config.toml (or PR_POOL_CONFIG), or the built-in default set
// when no config file is present. Role identity lives ONLY in config / built-in
// defaults — there is no env overlay for role fields (spec C).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

type Config struct {
	RepoRoot      string
	BeadsPrefix   string
	WorktreeDir   string
	SkillMD       string
	WorkerSkillMD string
	MaxFeedback   int
	MaxWorker     int
	MaxWait       time.Duration
	PollInterval  time.Duration
	// RetryBackoff is the pool-wide DEFAULT handler retry cadence (INV-FAIL-2,
	// pg2-0c8yz): how long the core waits before re-offering a pre-accept
	// decline, before an event's own expiresAt bounds it. A per-role
	// [role.retry] table overlays this. Default: backoff.Default().
	RetryBackoff backoff.Policy
	// PullFailureBackoff / PullFailureRetries are the pool-wide DEFAULT
	// pull-source failure backoff (INV-FAIL-3): the cadence and attempt bound
	// discover.Produce consults when a scheduled query FAILS, distinct from
	// PollInterval's success-path cadence. A per-query [query.failure_backoff]
	// table overlays this. Default: backoff.Default() shape, Retries: 0 (fail
	// fast — unchanged from pg2-qq9v's original behavior unless a deployment
	// opts in).
	PullFailureBackoff backoff.Policy
	PullFailureRetries int
	QuotaPaused        string
	CICDDown           string
	Effort             string
	Model              string
	PermissionMode     string
	// AllowedTools is the claude --allowed-tools allowlist forwarded verbatim to
	// `ccpool new --allowed-tools`. Combined with PermissionMode=dontAsk it is the
	// worker's security boundary: any tool NOT matching an entry here is
	// auto-denied (no human prompt). Empty omits the flag (claude's own default
	// tool policy applies — used only when an operator deliberately clears it).
	// SECURITY-SENSITIVE: the default value in Default() requires human sign-off.
	AllowedTools  string
	SessionPrefix string

	// Autonomous, when true, passes `--autonomous` to `ccpool new` so workers'
	// AskUserQuestion is structurally blocked (no human to answer). Default true.
	// Can be disabled via PR_POOL_AUTONOMOUS=false for operator debugging.
	Autonomous bool

	// SelfLogin is the GitHub login the worker safety preamble asserts authorship
	// against. From [pool].self_login; falls back to `pg-pr config show` at the
	// orchestrator/precheck layer when unset.
	SelfLogin string

	// Roles is the resolved, validated role set (TOML [[role]] or the built-in
	// default set). ConfigPath is the resolved config file path (for `config --show`).
	Roles      roles.RoleSet
	ConfigPath string

	// Queries is the resolved producer set (TOML [[query]] or the built-in default
	// query set). Under the event model a role and a query are wired only through a
	// shared event-type string (role.Binds ∩ query.Emits); Validate rejects orphan
	// producers/consumers.
	Queries query.SourceSet

	// Budget watchdog (chunk B). Token/Cost <= 0 means unlimited.
	BudgetTokens int64
	BudgetCost   int64 // cents
	BudgetTime   time.Duration
	ReminderPct  float64
	CancelPct    float64
	HardPct      float64
	LogDir       string
	ReminderMsg  string
	WrapUpMsg    string

	// ConfirmIngest is the worker's initial-nudge ingestion-guard window, forwarded
	// to `ccpool reply --confirm-ingest`. If the model never starts a turn within it
	// the dispatch fails fast and hands the bead back unclaimed (pg2-yukh #1).
	// Bounded well under BudgetTime so a dropped nudge is caught early. 0 disables.
	ConfirmIngest time.Duration

	// Locator resolves whether a configured source's or handler's BACKING COMMAND
	// can be invoked — the one environment probe in Validate. nil means "the
	// package default" (PathLocator: resolve on PATH), mirroring how a nil
	// query.Env.Cmd falls back to query.OSCommander. It is NOT a config-file key:
	// it exists so the probe is substitutable, since a unit test must not depend on
	// which binaries the machine running it happens to have installed.
	Locator CommandLocator
}

// CCPoolCommand is the ccpool binary a ccpool-type handler runs through
// (internal/ccpool's CLIRunner invokes it). It lives here so the runner and the
// pre-runtime absent-backing-command check share one literal.
const CCPoolCommand = "ccpool"

// CommandLocator resolves whether a participant's backing command can be invoked.
// It is a one-method interface — the same seam idiom as query.Commander and
// beads.Runner, not a bare func field — so a test substitutes it wholesale.
type CommandLocator interface {
	// Locate returns nil iff name names a command this machine can invoke.
	Locate(name string) error
}

// PathLocator is the production CommandLocator: it resolves a bare name on PATH
// and a name containing a separator as a path (exec.LookPath's own rule).
type PathLocator struct{}

func (PathLocator) Locate(name string) error {
	_, err := exec.LookPath(name)
	return err
}

// defaultLocator is what a Config carrying no Locator falls back to. Production
// never reassigns it; it is a var only so this package's own tests can pin a
// hermetic stub in place of the real PATH probe.
var defaultLocator CommandLocator = PathLocator{}

// locator returns the Config's CommandLocator, defaulting to defaultLocator.
func (c Config) locator() CommandLocator {
	if c.Locator != nil {
		return c.Locator
	}
	return defaultLocator
}

// Default returns the built-in defaults (mirrors pr-pool.sh's ${VAR:-default}).
func Default() Config {
	cwd, _ := os.Getwd()
	state := stateHome()
	return Config{
		RepoRoot:      cwd,
		BeadsPrefix:   "zr",
		WorktreeDir:   state + "/pr-pool/worktrees",
		SkillMD:       "",
		WorkerSkillMD: "",
		MaxFeedback:   1,
		MaxWorker:     1,
		MaxWait:       1800 * time.Second,
		PollInterval:  10 * time.Second,
		RetryBackoff:  backoff.Default(),
		// PullFailureBackoff shares the same shape default; Retries stays 0
		// (fail fast) so an unconfigured deployment is byte-for-byte unchanged
		// from pg2-qq9v's original "a query failure must NOT masquerade as no
		// ready work" behavior.
		PullFailureBackoff: backoff.Default(),
		PullFailureRetries: 0,
		QuotaPaused:        "",
		CICDDown:           "",
		Effort:             "max",
		Model:              "",
		Autonomous:         true,      // workers are human-less; AskUserQuestion is structurally blocked via ccpool --autonomous
		PermissionMode:     "dontAsk", // deny-by-default: auto-DENY any tool outside AllowedTools, non-interactive. PR_POOL_PERMISSION_MODE=bypassPermissions is the opt-in escape for an attended/trusted run.
		// SECURITY-SENSITIVE default allowlist (HUMAN SIGN-OFF REQUIRED — see plan).
		// Minimum verbs an autonomous worker needs; deliberately NOT blanket Bash.
		// Per-entry rationale is in docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md.
		// Bash(pg-pr:*): the review role's ONLY completion action is to post the review
		// back via `pg-pr review submit` (pg-pr owns the GitHub write; the review prompt
		// forbids gh), so under dontAsk it MUST be allow-listed or the post-back is
		// auto-denied (pg2-vmbn7). This is a pool-wide, full-pg-pr grant "for now" to see
		// the flow work end-to-end; scoping tool access per role (read-only review vs
		// write-capable worker) is tracked in pg2-f9vcg.
		AllowedTools:  "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(pg-pr:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*)",
		SessionPrefix: "pr-pool-",
		BudgetTokens:  0,                // unlimited until ccpool N3
		BudgetCost:    0,                // unlimited until ccpool N3
		BudgetTime:    25 * time.Minute, // strictly < MaxWait (30m)
		ReminderPct:   0.725,
		CancelPct:     0.90,
		HardPct:       1.00,
		LogDir:        state + "/pr-pool",
		ReminderMsg:   "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}.",
		WrapUpMsg:     "Budget nearly exhausted for bead {{.BeadID}}. Stop now: commit your notes with bd comment {{.BeadID}}, then finish or hand back. Do not start new work on any other bead.",
		ConfirmIngest: 90 * time.Second, // catch a dropped initial nudge well under BudgetTime
	}
}

// Load returns Default() overlaid with PR_POOL_* environment variables (pool scalars
// only), then the resolved role set: the [[role]] array from the config file
// (PR_POOL_CONFIG, else <RepoRoot>/.pr-pool/config.toml), or the built-in default
// set when no file / no [[role]] is present. A present-but-malformed file, an
// unknown type, or a failed validation is a hard error (never a silent fallback).
func Load() (Config, error) {
	c := Default()
	// Pool-scalar env overlay. The legacy role-specific env vars
	// (PR_POOL_MAX_WORKER/MAX_FEEDBACK/*_ENABLED/*_SKILL_MD) are intentionally GONE:
	// role identity now lives only in config / built-in defaults (spec C decision 7).
	c.RepoRoot = envStr("PR_POOL_REPO_ROOT", c.RepoRoot)
	c.BeadsPrefix = envStr("PR_POOL_BEADS_PREFIX", c.BeadsPrefix)
	c.WorktreeDir = envStr("PR_POOL_WORKTREE_DIR", c.WorktreeDir)
	c.MaxWait = envSecs("PR_POOL_MAX_WAIT", c.MaxWait)
	c.PollInterval = envSecs("PR_POOL_POLL_INTERVAL", c.PollInterval)
	c.QuotaPaused = envStr("PR_POOL_QUOTA_PAUSED", c.QuotaPaused)
	c.CICDDown = envStr("PR_POOL_CICD_DOWN", c.CICDDown)
	c.Effort = envStr("PR_POOL_EFFORT", c.Effort)
	c.Model = envStr("PR_POOL_MODEL", c.Model)
	c.PermissionMode = envStr("PR_POOL_PERMISSION_MODE", c.PermissionMode)
	c.Autonomous = envBool("PR_POOL_AUTONOMOUS", c.Autonomous)
	c.AllowedTools = envStr("PR_POOL_ALLOWED_TOOLS", c.AllowedTools)
	c.SessionPrefix = envStr("PR_POOL_SESSION_PREFIX", c.SessionPrefix)
	c.BudgetTokens = int64(envInt("PR_POOL_BUDGET_TOKENS", int(c.BudgetTokens)))
	c.BudgetCost = int64(envInt("PR_POOL_BUDGET_COST", int(c.BudgetCost)))
	c.BudgetTime = envSecs("PR_POOL_BUDGET_TIME", c.BudgetTime)
	c.ConfirmIngest = envSecs("PR_POOL_CONFIRM_INGEST", c.ConfirmIngest)
	c.LogDir = envStr("PR_POOL_LOG_DIR", c.LogDir)

	// XDG-global budget layer: sits BENEATH the repo-local file but ABOVE env.
	// Contributes [pool].budget only; absent/empty file = no change. The path is
	// overridable via PR_POOL_GLOBAL_CONFIG (test seam, mirrors PR_POOL_CONFIG).
	globalReg := NewRegistry()
	globalPath := envStr("PR_POOL_GLOBAL_CONFIG", filepath.Join(configHome(), "pr-pool", "config.toml"))
	if _, statErr := os.Stat(globalPath); statErr == nil {
		if err := globalReg.decodeGlobalBudget(globalPath, &c); err != nil {
			return Config{}, err
		}
		slog.Info("loaded pr-pool global budget config", "path", globalPath)
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("stat %s: %w", globalPath, statErr)
	}

	path := envStr("PR_POOL_CONFIG", filepath.Join(c.RepoRoot, ".pr-pool", "config.toml"))
	c.ConfigPath = path
	reg := NewRegistry()
	if _, statErr := os.Stat(path); statErr == nil {
		rs, err := reg.decodeRoleSet(path, filepath.Dir(path), &c)
		if err != nil {
			return Config{}, err
		}
		if rs != nil {
			c.Roles = rs
			slog.Info("loaded pr-pool config", "path", path, "roles", len(rs))
		} else {
			slog.Info("pr-pool config present but defines no [[role]]; using built-in roles", "path", path)
		}
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("stat %s: %w", path, statErr)
	} else {
		slog.Info("no pr-pool config found; using built-in roles", "path", path)
	}
	if c.Roles == nil {
		bp := roles.BuiltinParams{
			WorktreeDir:   c.WorktreeDir,
			SkillMD:       c.SkillMD,
			WorkerSkillMD: c.WorkerSkillMD,
			MaxFeedback:   c.MaxFeedback,
			MaxWorker:     c.MaxWorker,
			WorkerBudget:  c.WorkerBudget(),
			PollInterval:  c.PollInterval,
		}
		c.Roles = roles.BuiltinRoleSet(bp)
		// The built-in query set is paired with the built-in roles (feedback query
		// emits feedback.ready, feedback role binds it, ...) — reproducing today's
		// coupled role+query behavior through the event model.
		c.Queries = roles.BuiltinQuerySet(bp)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validPermissionModes is the set of claude --permission-mode values pr-pool may
// pass through to `ccpool new` (mirrors ccpool's launch.PermissionMode; it is
// duplicated here because pr-pool does not depend on the ccpool module). The
// empty string is valid: it means "omit the flag".
var validPermissionModes = map[string]bool{
	"":                  true,
	"default":           true,
	"acceptEdits":       true,
	"plan":              true,
	"auto":              true,
	"dontAsk":           true,
	"bypassPermissions": true,
}

// Validate runs the PRE-RUNTIME wiring checks and blocks on anything determinable
// as an invalid configuration. Six conditions are blocking, and they are the ones
// docs/behavior states (INV-WORKFLOW-1, USECASE-VALIDATE-CONFIG):
//
//  1. orphan event type          — a binding matches a type no source emits
//  2. unhandled source output    — a source emits a type no binding declares
//  3. disconnected handler       — a handler no binding can reach
//  4. handler with no events     — a BOUND handler whose reachable event set is empty
//  5. absent backing command     — a source's/handler's command cannot be invoked
//  6. non-terminating re-entry   — a cycle the declared graph shows cannot terminate
//
// plus PermissionMode and each resolved query's own Validate. Errors are
// AGGREGATED (errors.Join), never early-returned, so a bad config reports every
// problem at once at pre-flight.
//
// EXACTLY ONE condition warns instead of blocking — a re-entry cycle whose
// termination is NOT determinable — and that category is closed at one member:
// nothing else here may warn. The warning goes to slog.Warn, the channel this
// layer already uses for pre-flight diagnostics (see cmd/pr-pool's stub-query and
// tracked-config warnings), so reporting it costs no caller a signature change.
//
// RUN-SCOPING IS NOT A CONFIG DEFECT: validity is judged against the
// configuration and never against the run's active subset, so a source or handler
// merely disabled for this run is neither an error nor the warning. That is why
// nothing below reads Role.Enabled and why Validate takes no run-scope argument.
func (c Config) Validate() error {
	errs, warns := c.diagnose()
	for _, w := range warns {
		slog.Warn("pre-runtime wiring warning; reported, and the run proceeds", "finding", w)
	}
	return errors.Join(errs...)
}

// diagnose is Validate's whole check set, split so tests can assert the BLOCKING
// findings and the non-blocking WARNING separately (Validate itself can only
// return the errors).
func (c Config) diagnose() (errs []error, warns []string) {
	if !validPermissionModes[c.PermissionMode] {
		errs = append(errs, fmt.Errorf("invalid PR_POOL_PERMISSION_MODE %q (valid: default, acceptEdits, plan, auto, dontAsk, bypassPermissions)", c.PermissionMode))
	}
	// emitted collects every event type produced by some query; bound collects
	// every event type consumed by some role.
	emitted := map[string]bool{}
	for _, s := range c.Queries {
		if s.Query == nil {
			continue
		}
		if err := s.Query.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("query %q: %w", s.Name, err))
		}
		for _, e := range s.Query.Emits() {
			emitted[e] = true
		}
	}
	bound := map[string]bool{}
	for _, role := range c.Roles {
		for _, b := range role.Binds {
			bound[b] = true
			if !emitted[b] {
				errs = append(errs, fmt.Errorf("role %q binds event type %q that no query emits (orphan consumer)", role.Name, b))
			}
		}
		if handlerIsDisconnected(role) {
			errs = append(errs, fmt.Errorf("role %q binds no event type, so no query can reach it (disconnected handler)", role.Name))
		}
		if handlerHasNoEventsToListenFor(role, emitted) {
			errs = append(errs, fmt.Errorf("role %q is bound, but every event type it binds (%s) is emitted by no query, so it can never receive an event (handler with no events to listen for)",
				role.Name, strings.Join(role.Binds, ", ")))
		}
	}
	for _, s := range c.Queries {
		if s.Query == nil {
			continue
		}
		for _, e := range s.Query.Emits() {
			if !bound[e] {
				errs = append(errs, fmt.Errorf("query %q emits event type %q that no role binds (orphan producer)", s.Name, e))
			}
		}
	}
	errs = append(errs, c.absentBackingCommands()...)
	cycleErrs, cycleWarns := c.reentryCycleFindings()
	errs = append(errs, cycleErrs...)
	warns = append(warns, cycleWarns...)
	return errs, warns
}

// handlerIsDisconnected is check 3 — "a handler no binding can reach". A binding
// is not a first-class object here: it is the handler's own Binds list, so the
// only way no binding reaches a handler is for that list to be empty. (A TOML
// role cannot reach this state — buildRole requires binds — so this guards the
// role sets built in Go, e.g. roles.BuiltinRoleSet.)
func handlerIsDisconnected(role roles.Role) bool { return len(role.Binds) == 0 }

// handlerHasNoEventsToListenFor is check 4, and it is the ONE place that check's
// definition lives — the reading that landed in docs/behavior is "a BOUND handler
// whose reachable event set is empty: its binding declares no type, or every type
// it binds is emitted by no configured source". Revise the definition here and
// nowhere else.
//
// The "declares no type" half is NOT separable from check 3 in this config model
// (both are len(Binds) == 0, because the binding is inlined into the handler), so
// this deliberately does not fire for an unbound handler — that condition is
// reported once, as check 3, rather than twice. Overlapping with check 1 IS
// intended: check 1 names the unemitted TYPE and this names the HANDLER, so a
// handler bound only to orphan types is reported both ways.
func handlerHasNoEventsToListenFor(role roles.Role, emitted map[string]bool) bool {
	if len(role.Binds) == 0 {
		return false
	}
	for _, b := range role.Binds {
		if emitted[b] {
			return false
		}
	}
	return true
}

// absentBackingCommands is check 5 — every configured handler's and source's
// backing command must be invocable. This is the only check that probes the
// ENVIRONMENT rather than the configuration, which is why the probe is injected
// (Config.Locator) instead of calling exec.LookPath inline. A participant that
// declares no backing command (an in-process event source) is skipped.
func (c Config) absentBackingCommands() []error {
	loc := c.locator()
	var errs []error
	for _, role := range c.Roles {
		cmd := handlerBackingCommand(role)
		if cmd == "" {
			continue
		}
		if err := loc.Locate(cmd); err != nil {
			errs = append(errs, fmt.Errorf("handler %q backing command %q cannot be invoked: %w (absent backing command)", role.Name, cmd, err))
		}
	}
	for _, s := range c.Queries {
		if s.Query == nil {
			continue
		}
		cmd := s.Query.BackingCommand()
		if cmd == "" {
			continue
		}
		if err := loc.Locate(cmd); err != nil {
			errs = append(errs, fmt.Errorf("source %q backing command %q cannot be invoked: %w (absent backing command)", s.Name, cmd, err))
		}
	}
	return errs
}

// handlerBackingCommand returns the executable a role runs through: its own
// argv[0] for a command role, the ccpool binary for a ccpool role. "" means the
// role declares none.
func handlerBackingCommand(role roles.Role) string {
	switch role.Type {
	case "command":
		if role.Command != nil && len(role.Command.Argv) > 0 {
			return role.Command.Argv[0]
		}
	case "ccpool":
		return CCPoolCommand
	}
	return ""
}

// reentryCycleFindings walks the declared routing graph for re-entry cycles and
// splits them on the determinability line: a cycle the graph shows CANNOT
// terminate is check 6 (blocking), and a cycle whose termination is NOT
// determinable is this set's one warning.
//
// The graph has two edge kinds, and they are the only re-entry edges the
// CONFIGURATION states:
//
//	source --emits--> type      (Query.Emits)
//	type --triggers--> source   (a ThresholdTrigger's Binds)
//
// A handler's own output is deliberately NOT an edge: the core never sees a
// handler's outcome, so handler-produced work re-enters only through a source,
// and a threshold trigger is how the configuration declares that re-entry.
//
// Determinability: the driver fires a threshold query when the queued depth of
// its bound types is >= Count, so a gate with Count <= 0 is satisfied by ZERO
// events and can never withhold — a cycle containing one re-fires unconditionally
// and the graph therefore SHOWS it cannot terminate. Any other gate depends on how
// many events a source actually produces at runtime, which the configuration does
// not state, so the graph determines nothing and the cycle warns.
//
// One finding per cycle: the nodes of a reported cycle are marked, so overlapping
// cycles through the same nodes do not multiply into a wall of findings.
func (c Config) reentryCycleFindings() (errs []error, warns []string) {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	adj := map[string][]string{}
	label := map[string]string{}
	unconditional := map[string]bool{}
	var order []string
	node := func(id, lab string) string {
		if _, ok := label[id]; !ok {
			label[id] = lab
			order = append(order, id)
		}
		return id
	}
	typeNode := func(t string) string { return node("t:"+t, fmt.Sprintf("event type %q", t)) }
	for i, s := range c.Queries {
		if s.Query == nil {
			continue
		}
		src := node("s:"+strconv.Itoa(i), fmt.Sprintf("source %q", s.Name))
		for _, e := range s.Query.Emits() {
			adj[src] = append(adj[src], typeNode(e))
		}
		if tt, ok := query.Threshold(s.Query.Trigger()); ok {
			if tt.Count <= 0 {
				unconditional[src] = true
			}
			for _, b := range tt.Binds {
				t := typeNode(b)
				adj[t] = append(adj[t], src)
			}
		}
	}
	state := map[string]int{}
	reported := map[string]bool{}
	var stack []string
	var walk func(id string)
	walk = func(id string) {
		state[id] = onStack
		stack = append(stack, id)
		for _, next := range adj[id] {
			switch state[next] {
			case unvisited:
				walk(next)
			case onStack:
				if reported[next] {
					continue
				}
				from := len(stack) - 1
				for i, n := range stack {
					if n == next {
						from = i
						break
					}
				}
				cycle := stack[from:]
				path := make([]string, 0, len(cycle)+1)
				blocking := false
				for _, n := range cycle {
					reported[n] = true
					blocking = blocking || unconditional[n]
					path = append(path, label[n])
				}
				path = append(path, label[cycle[0]])
				joined := strings.Join(path, " -> ")
				if blocking {
					errs = append(errs, fmt.Errorf("re-entry cycle %s cannot terminate: a threshold trigger on it fires with no events required (determinably non-terminating re-entry cycle)", joined))
				} else {
					warns = append(warns, fmt.Sprintf("re-entry cycle %s: the declared graph cannot determine whether it terminates", joined))
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
	}
	for _, id := range order {
		if state[id] == unvisited {
			walk(id)
		}
	}
	return errs, warns
}

// WorkerBudget assembles the per-worker Budget from config scalars + the default
// price table. Used as the pool-default budget for built-in roles and as the base
// a per-role [role.ccpool].budget overlays.
func (c Config) WorkerBudget() budget.Budget {
	return budget.Budget{
		Tokens:     budget.Limit(c.BudgetTokens),
		Cost:       budget.Limit(c.BudgetCost),
		Time:       c.BudgetTime,
		Thresholds: budget.Thresholds{Reminder: c.ReminderPct, Cancel: c.CancelPct, Hard: c.HardPct},
		Prices:     usage.DefaultPrices(),
	}
}

// LogDir resolves ONLY the log/state directory — Default() overlaid with
// PR_POOL_LOG_DIR — without loading, parsing or validating config.toml.
//
// It exists for the entry points that need nothing but the state directory and
// must not be able to fail on unrelated config: the core's socket + discovery
// record live under LogDir, so a manager→core callback (`ingest-event`) has to be
// able to FIND a running core even when the repo-local config.toml is missing or
// broken. Load() remains the full resolution for everything else.
func LogDir() string {
	return envStr("PR_POOL_LOG_DIR", Default().LogDir)
}

func stateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/.local/state"
}

// configHome resolves the XDG config base dir for the pr-pool global config file,
// mirroring stateHome(). XDG_CONFIG_HOME wins; otherwise ~/.config.
func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/.config"
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// envBool overlays a bool from env: "false"/"0"/"no" → false, "true"/"1"/"yes" →
// true; an unset or unparseable value keeps def.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envSecs(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return def
}
