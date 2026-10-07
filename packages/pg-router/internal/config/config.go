// Package config holds pg-router's runtime configuration. Pool scalars layer
// Default() -> PG_ROUTER_* env -> [pool] TOML (the config file wins for the keys it
// sets: self_login, worktree_dir, budget) —
// [pool] wins over PG_ROUTER_* env, which wins over the built-in default. Roles
// come from the [[role]] array in <RepoRoot>/.pg-router/config.toml (or
// PG_ROUTER_CONFIG), or the built-in default set when no config file is present.
// Role identity lives ONLY in config / built-in defaults — there is no env
// overlay for role fields (spec C).
package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/x/gitclient"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
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
	// SerializeTypes is the set of event TYPES marked to serialize (INV-CONC-1,
	// `packages/pg-router/docs/decisions · DEC-CONC-1`): the core offers at most
	// one event of a marked type at a time, across every bound handler, until
	// it is released (settled for every currently-bound handler — accepted, or
	// given its one attempt past expiresAt). From [pool].serialize_types; empty
	// (the default) marks nothing, so an existing deployment's dispatch is
	// unchanged.
	SerializeTypes []string
	// CompactThresholdBytes is the queue.jsonl size above which the queue starts
	// a background compaction of its write-ahead log down to live state
	// (eventqueue.WithCompaction, bead pg2-8e0m6). From [pool].compact_threshold_bytes
	// or PG_ROUTER_COMPACT_THRESHOLD_BYTES; Default() is DefaultCompactThresholdBytes.
	// 0 disables RUNTIME compaction only — the log is still compacted once at
	// startup. After a compaction the trigger rises to twice the compacted size
	// when that is larger, so a live set bigger than the threshold cannot make
	// the queue compact on every sweep.
	CompactThresholdBytes int64
	// MaxLogBytes is the HARD size limit of the queue.jsonl write-ahead log
	// (bead pg2-5d3ui): at or above it, new events are rejected at ingest with a
	// classified log_full reason. A derived SOFT threshold (SoftLogBytes, 90% of
	// it) halts the polled emitters first. From [pool].max_log_bytes or
	// PG_ROUTER_MAX_LOG_BYTES (which accepts units: 64MiB, 100MB, 512KiB, ...);
	// Default() is DefaultMaxLogBytes. Zero or negative is rejected by Validate
	// (a limit is always in force), as is an order other than
	// compact_threshold_bytes < soft < max_log_bytes.
	MaxLogBytes int64
	// The three file-backed INV-LIFE-2 gates (OperatorPaused / CICDDown /
	// DiskSpaceLow), their external disable kill-switches and the
	// PG_ROUTER_OPERATOR_PAUSED / PG_ROUTER_CICD_DOWN / PG_ROUTER_DISK_SPACE_LOW[_DISABLE]
	// env vars and [pool] *_path keys that configured them are GONE (bead
	// pg2-h63eu): gates are now generic TYPE-keyed records in the event log (Gate
	// Registry, internal/eventqueue/gate.go), set and cleared through the gate
	// API/CLI. Clearing a gate is the veto, so no disable-override mechanism
	// remains. See MIGRATION.md's "Gates: file-backed gates replaced by the Gate
	// Registry".
	Effort string
	Model  string
	// PermissionMode is an OPAQUE, un-validated string on this side of the wire
	// boundary (docket pg2-oju6w Task 5.7): pg-router forwards it verbatim to
	// the new module's own dispatch config and displays it in `config --show`,
	// but no longer checks it against the claude --permission-mode enum — that
	// check now lives in packages/pg-router-ccpool-handler/internal/config,
	// the side that actually invokes `ccpool new --permission-mode`.
	PermissionMode string
	// AllowedTools is the claude --allowed-tools allowlist forwarded verbatim to
	// `ccpool new --allowed-tools`. Combined with PermissionMode=dontAsk it is the
	// worker's security boundary: any tool NOT matching an entry here is
	// auto-denied (no human prompt). Empty omits the flag (claude's own default
	// tool policy applies — used only when an operator deliberately clears it).
	// SECURITY-SENSITIVE: the default value in Default() requires human sign-off.
	AllowedTools string
	// PRTool is the external tool a review role's completion action posts its
	// review back through, and reads PR facts from (docket pg2-oju6w's Task
	// 5.13 register-catch-down / GOAL-MIN-1's Floor: pg-router's own contract
	// surface names no concrete tool). Empty (the default) adds no extra grant
	// to the built-in AllowedTools default — see defaultAllowedTools below. A
	// deployment that wants review-post capability sets PG_ROUTER_PR_TOOL (or
	// configures AllowedTools directly). Unlike AllowedTools, PRTool carries
	// no [pool] TOML key today — env-only.
	PRTool        string
	SessionPrefix string

	// HandlerCommand is the argv PREFIX cmd/pg-router invokes (Task 5.4's
	// wireclient.CommandFor seam, cmd/pg-router/run.go's handlerCommandFor)
	// for EVERY enabled role's registered handler participant, over
	// internal/wireclient's DEC-WIRE-1 CLI transport — the "deployment
	// concern" wireclient's own package doc and ADR 0065's Addendum both
	// forward-reference (bead pg2-g068j resolves it). It carries NO
	// baked-in default: GOAL-MIN-1's Floor (ADR 0065's Register row R16)
	// requires pg-router's own contract surface — --help, config
	// validation, built-in defaults — to name no concrete tool, so an
	// unconfigured deployment gets a clear per-dispatch error
	// ("wireclient: resolve command for role ...") rather than this binary
	// silently invoking a hardcoded participant name. Env-only
	// (PG_ROUTER_HANDLER_COMMAND), mirroring PRTool's own env-only wiring —
	// no [pool] TOML key today. Every enabled role resolves to the SAME
	// command today (DEC-WIRE-3's "shared process backing multiple roles"
	// is an accepted shape, not a defect this seam needs to solve).
	HandlerCommand string

	// HandlerCommandDir (PG_ROUTER_HANDLER_COMMAND_DIR), when set, is a
	// directory of per-role JSON files (<role.Name>.json, the same roleFile
	// shape packages/pg-router-ccpool-handler/cmd's roleconfig.go already
	// decodes) that lets differently-configured roles sharing ONE
	// HandlerCommand binary (e.g. feedback/worker/review, each with its own
	// ccpool actor/prompt) each dispatch through their OWN participant
	// config, closing the gap HandlerCommand's own doc comment above
	// accepted as a shape ("every enabled role resolves to the SAME command
	// today") — this bead, pg2-ymb3v. cmd/pg-router/run.go's
	// handlerCommandFor resolves [HandlerCommand, subcommand,
	// "--role-config", filepath.Join(HandlerCommandDir, role.Name+".json")]
	// when this is set, and falls back to today's [HandlerCommand,
	// subcommand] unchanged when it is not — so an existing single-role
	// deployment setting only PG_ROUTER_HANDLER_COMMAND is unaffected.
	// Env-only, no [pool] TOML key, mirroring HandlerCommand's own env-only
	// wiring.
	HandlerCommandDir string

	// Autonomous, when true, passes `--autonomous` to `ccpool new` so workers'
	// AskUserQuestion is structurally blocked (no human to answer). Default true.
	// Can be disabled via PG_ROUTER_AUTONOMOUS=false for operator debugging.
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

	// ExpectedIntervalOverrides is query name -> an explicit `expected_interval`
	// TOML override, in milliseconds (this task, pg2-mnf7t.1). Populated by
	// Registry.buildQueries from each [[query]]'s own expected_interval key;
	// config.ExpectedIntervalMsFor(src, this map) is the resolver
	// cmd/pg-router's bootCore calls to build core.Options.SourceIntervalsMs.
	// nil/absent (the default, and every query that declares no override)
	// resolves to "fall back to the query's own trigger, or unknown" —
	// ExpectedIntervalMsFor's own doc comment.
	ExpectedIntervalOverrides map[string]int64

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

	// MeterProvider is the OTel MeterProvider binding seam (INV-OBS-1: the core
	// stays unaware of any concrete monitoring backend — a deployment binds a
	// real one here). nil selects the package default: the OTel no-op provider,
	// CHOSEN BY CONFIG rather than hardcoded in code (Task 3.3 binding
	// decision). It is NOT a config-file key, the same posture Locator already
	// takes for the backing-command probe: which monitoring backend to use is a
	// deployment/runtime binding decision, not something declared in
	// .pg-router/config.toml.
	MeterProvider metric.MeterProvider

	// ActivityRingSize is the dispatch-outcome ring buffer's capacity
	// (internal/activity.Ring, Task 3.4), from PG_ROUTER_ACTIVITY_RING. 0 (the
	// zero value, and Default()'s own default) selects internal/activity's
	// own package default (currently 512) — the same nil/zero-means-package-
	// default idiom Locator and MeterProvider already use above, so this
	// package does not need to import internal/activity just to duplicate
	// its constant.
	ActivityRingSize int

	// MonitorSubsets resolves a `mon.read` registration id (INTF-MON,
	// registry.KindMonitor) to the metric catalog subset it may read — by
	// metric NAME, not this package's concern to define the vocabulary of
	// (Task 3.6-prereq: "resolved BEFORE a mon.read caller ever calls
	// register... looked up from config by registration id, not carried on
	// the mon.read request itself"). nil/absent (the default, and every
	// deployment that declares no [[monitor]] entry) resolves every id to no
	// subset — the same nil-means-package-default idiom
	// MeterProvider/Locator/ActivityRingSize above already use.
	//
	// Populated from the [[monitor]] TOML array (pg2-nhvdo): each entry's
	// `id` becomes a map key and its `subset` (a list of metric names) the
	// value, mirroring how [[role]]/[[query]] populate Roles/Queries. Like
	// role identity, there is no env overlay — this is a config-file-only
	// key. cmd/pg-router's bootCore threads the resulting map into
	// internal/core.Options.MonitorSubsets via monitorSubsetResolverFrom.
	MonitorSubsets map[string][]string

	// MetricsAddr is the listen address (host:port) for the OTel Prometheus
	// /metrics direct-scrape HTTP endpoint (design decision D2), from
	// PG_ROUTER_METRICS_ADDR. Empty (the default, and Default()'s own
	// default) leaves it disabled — no listener is opened and
	// resolveMeterProvider's existing package-default provider stands
	// unchanged (cmd/pg-router/run.go). Like MonitorSubsets above, it is a
	// deployment/runtime concern, not a [pool] TOML key: cmd/pg-router's
	// `run --metrics-addr` flag OVERRIDES this env-sourced value, the same
	// "CLI flag > PG_ROUTER_* env > built-in default" precedence
	// PG_ROUTER_TUI_INTERVAL already documents (args.go).
	MetricsAddr string
}

// Meter returns the Config's MeterProvider, defaulting to the OTel no-op
// provider when unset (mirrors locator()'s pattern for CommandLocator).
// Exported — unlike locator() — because cmd/pg-router's bootCore (a different
// package) is where the seam is actually resolved into the provider `New`
// wires into the metrics Emitter and the queue/core observer.
func (c Config) Meter() metric.MeterProvider {
	if c.MeterProvider != nil {
		return c.MeterProvider
	}
	return noop.NewMeterProvider()
}

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

// baseAllowedTools is the built-in claude --allowed-tools allowlist granted to
// every autonomous worker regardless of configuration (HUMAN SIGN-OFF
// REQUIRED — see plan). Minimum verbs an autonomous worker needs; deliberately
// NOT blanket Bash. Per-entry rationale is in
// docs/superpowers/plans/2026-06-23-pg-router-deny-by-default-allowlist.md.
const baseAllowedTools = "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*)"

// defaultAllowedTools builds the SECURITY-SENSITIVE AllowedTools default:
// baseAllowedTools plus, when prTool is configured, a Bash(<prTool>:*) grant.
// A review role's ONLY completion action is to post its review back through
// that external tool (which owns the actual write; the review prompt forbids
// any other write path), so under dontAsk deny-by-default that grant MUST be
// present or the post-back is auto-denied (pg2-vmbn7). Which tool that is is
// deployment configuration (PRTool / PG_ROUTER_PR_TOOL) — unlike bd (this
// pool's own toolbox), it is not a name pg-router's own contract surface
// bakes in. Empty prTool omits the grant entirely; scoping tool access per
// role (read-only review vs write-capable worker) is tracked in pg2-f9vcg.
func defaultAllowedTools(prTool string) string {
	if prTool == "" {
		return baseAllowedTools
	}
	return baseAllowedTools + ",Bash(" + prTool + ":*)"
}

// Default returns the built-in defaults (mirrors pg-router.sh's ${VAR:-default}).
func Default() Config {
	cwd, _ := os.Getwd()
	state := stateHome()
	return Config{
		RepoRoot:              cwd,
		BeadsPrefix:           "zr",
		WorktreeDir:           state + "/pg-router/worktrees",
		SkillMD:               "",
		WorkerSkillMD:         "",
		MaxFeedback:           1,
		MaxWorker:             1,
		MaxWait:               1800 * time.Second,
		PollInterval:          10 * time.Second,
		RetryBackoff:          backoff.Default(),
		CompactThresholdBytes: DefaultCompactThresholdBytes,
		MaxLogBytes:           DefaultMaxLogBytes,
		// PullFailureBackoff shares the same shape default; Retries stays 0
		// (fail fast) so an unconfigured deployment is byte-for-byte unchanged
		// from pg2-qq9v's original "a query failure must NOT masquerade as no
		// ready work" behavior.
		PullFailureBackoff: backoff.Default(),
		PullFailureRetries: 0,
		Effort:             "max",
		Model:              "",
		Autonomous:         true,      // workers are human-less; AskUserQuestion is structurally blocked via ccpool --autonomous
		PermissionMode:     "dontAsk", // deny-by-default: auto-DENY any tool outside AllowedTools, non-interactive. PG_ROUTER_PERMISSION_MODE=bypassPermissions is the opt-in escape for an attended/trusted run.
		PRTool:             "",        // no review-post grant by default — see defaultAllowedTools's doc comment
		AllowedTools:       defaultAllowedTools(""),
		HandlerCommand:     "", // no baked-in handler participant name — GOAL-MIN-1's Floor; see HandlerCommand's doc comment
		SessionPrefix:      "pg-router-",
		BudgetTokens:       0,                // unlimited until ccpool N3
		BudgetCost:         0,                // unlimited until ccpool N3
		BudgetTime:         25 * time.Minute, // strictly < MaxWait (30m)
		ReminderPct:        0.725,
		CancelPct:          0.90,
		HardPct:            1.00,
		LogDir:             state + "/pg-router",
		ReminderMsg:        "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}.",
		WrapUpMsg:          "Budget nearly exhausted for bead {{.BeadID}}. Stop now: commit your notes with bd comment {{.BeadID}}, then finish or hand back. Do not start new work on any other bead.",
		ConfirmIngest:      90 * time.Second, // catch a dropped initial nudge well under BudgetTime
	}
}

// Load returns Default() overlaid with PG_ROUTER_* environment variables (pool scalars
// only), then the resolved role set: the [[role]] array from the config file
// (PG_ROUTER_CONFIG, else <RepoRoot>/.pg-router/config.toml resolved via
// resolveConfigPath's git-common-dir read-through), or ZERO roles/queries
// when no file / no [[role]] is present (docket pg2-oju6w's Task 5.8 deleted
// the former built-in feedback/worker/review fallback — see this function's
// own c.Validate() preamble below). A present-but-malformed file, an unknown
// type, or a failed validation is a hard error (never a silent fallback).
// The no-file case itself is not an error: it logs at WARN (or INFO when
// PG_ROUTER_NO_CONFIG_WARN opts out — pg2-xl659) and leaves Roles/Queries nil.
func Load() (Config, error) {
	c := Default()
	// Pool-scalar env overlay. The legacy role-specific env vars
	// (PG_ROUTER_MAX_WORKER/MAX_FEEDBACK/*_ENABLED/*_SKILL_MD) are intentionally GONE:
	// role identity now lives only in config / built-in defaults (spec C decision 7).
	c.RepoRoot = envStr("PG_ROUTER_REPO_ROOT", c.RepoRoot)
	c.BeadsPrefix = envStr("PG_ROUTER_BEADS_PREFIX", c.BeadsPrefix)
	c.WorktreeDir = envStr("PG_ROUTER_WORKTREE_DIR", c.WorktreeDir)
	c.MaxWait = envSecs("PG_ROUTER_MAX_WAIT", c.MaxWait)
	c.PollInterval = envSecs("PG_ROUTER_POLL_INTERVAL", c.PollInterval)
	c.Effort = envStr("PG_ROUTER_EFFORT", c.Effort)
	c.Model = envStr("PG_ROUTER_MODEL", c.Model)
	c.PermissionMode = envStr("PG_ROUTER_PERMISSION_MODE", c.PermissionMode)
	c.Autonomous = envBool("PG_ROUTER_AUTONOMOUS", c.Autonomous)
	// PRTool overlays BEFORE AllowedTools resolves, so a PG_ROUTER_PR_TOOL set
	// without an explicit PG_ROUTER_ALLOWED_TOOLS still gets its Bash(<tool>:*)
	// grant folded into the built-in default.
	c.PRTool = envStr("PG_ROUTER_PR_TOOL", c.PRTool)
	c.AllowedTools = envStr("PG_ROUTER_ALLOWED_TOOLS", defaultAllowedTools(c.PRTool))
	c.SessionPrefix = envStr("PG_ROUTER_SESSION_PREFIX", c.SessionPrefix)
	c.HandlerCommand = envStr("PG_ROUTER_HANDLER_COMMAND", c.HandlerCommand)
	c.HandlerCommandDir = envStr("PG_ROUTER_HANDLER_COMMAND_DIR", c.HandlerCommandDir)
	c.BudgetTokens = int64(envInt("PG_ROUTER_BUDGET_TOKENS", int(c.BudgetTokens)))
	c.BudgetCost = int64(envInt("PG_ROUTER_BUDGET_COST", int(c.BudgetCost)))
	c.BudgetTime = envSecs("PG_ROUTER_BUDGET_TIME", c.BudgetTime)
	c.ConfirmIngest = envSecs("PG_ROUTER_CONFIRM_INGEST", c.ConfirmIngest)
	c.LogDir = envStr("PG_ROUTER_LOG_DIR", c.LogDir)
	c.ActivityRingSize = envInt("PG_ROUTER_ACTIVITY_RING", c.ActivityRingSize)
	c.CompactThresholdBytes = int64(envInt("PG_ROUTER_COMPACT_THRESHOLD_BYTES", int(c.CompactThresholdBytes)))
	maxLog, err := envBytes("PG_ROUTER_MAX_LOG_BYTES", c.MaxLogBytes)
	if err != nil {
		return Config{}, err
	}
	c.MaxLogBytes = maxLog
	c.MetricsAddr = envStr("PG_ROUTER_METRICS_ADDR", c.MetricsAddr)

	// XDG-global budget layer: sits BENEATH the repo-local file but ABOVE env.
	// Contributes [pool].budget only; absent/empty file = no change. The path is
	// overridable via PG_ROUTER_GLOBAL_CONFIG (test seam, mirrors PG_ROUTER_CONFIG).
	globalReg := NewRegistry()
	globalPath := envStr("PG_ROUTER_GLOBAL_CONFIG", filepath.Join(configHome(), "pg-router", "config.toml"))
	if _, statErr := os.Stat(globalPath); statErr == nil {
		if err := globalReg.decodeGlobalBudget(globalPath, &c); err != nil {
			return Config{}, err
		}
		slog.Info("loaded pg-router global budget config", "path", globalPath)
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("stat %s: %w", globalPath, statErr)
	}

	path := envStr("PG_ROUTER_CONFIG", resolveConfigPath(c.RepoRoot))
	c.ConfigPath = path
	reg := NewRegistry()
	if _, statErr := os.Stat(path); statErr == nil {
		rs, err := reg.decodeRoleSet(path, filepath.Dir(path), &c)
		if err != nil {
			return Config{}, err
		}
		if rs != nil {
			c.Roles = rs
			slog.Info("loaded pg-router config", "path", path, "roles", len(rs))
		} else {
			slog.Info("pg-router config present but defines no [[role]]; running with zero roles and zero queries", "path", path)
		}
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("stat %s: %w", path, statErr)
	} else if noConfigWarnSuppressed() {
		slog.Info("no pg-router config found; running with zero roles and zero queries until configured", "path", path)
	} else {
		slog.Warn("no pg-router config found; running with zero roles and zero queries until configured (set PG_ROUTER_NO_CONFIG_WARN=true to silence this)", "path", path)
	}
	// The built-in feedback/worker/review role+query fallback (roles.
	// BuiltinRoleSet/BuiltinQuerySet) is DELETED here (docket pg2-oju6w's
	// Task 5.8, ADR 0065's "Source-side boundary" section, closing register
	// row USECASE-CREATE-SOURCE / bead pg2-u7rzl): an unconfigured core
	// (config.toml absent, or present with no [[role]]) now runs with c.Roles
	// and c.Queries left at their zero value (nil) — zero roles, zero
	// queries, doing nothing until configured. The beads-shaped role/query
	// pairing this fallback used to provide now lives as a registered
	// kind:"source" participant in packages/pg-router-ccpool-handler, wired
	// through pg-router's own [[query]]/[[role]] TOML (query.ParticipantQuery),
	// never as an automatic built-in.
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// resolveConfigPath resolves the DEFAULT .pg-router/config.toml location for
// repoRoot: <canonical-clone-root>/.pg-router/config.toml, found by running
// `git rev-parse --git-common-dir` FROM repoRoot (pg2-xl659 design decision,
// operator 2026-09-10: read-through, not a per-worktree symlink-in bootstrap
// — this deliberately does NOT follow the per-worktree .pre-commit-config.yaml
// symlink precedent that pre-hook-bundle repos once used for a different
// problem; pb drain isolate and wtnew no longer link any hook config).
// A linked worktree's own .pg-router/ is never consulted: --git-common-dir
// always resolves to the ONE canonical clone regardless of which worktree
// pg-router is invoked from, so there is no multi-worktree ambiguity to design
// around, and the resolution stays correct if the canonical config later
// changes (no stale copy to go out of sync).
//
// This is a soft-fail probe, the same posture gitfacet.Resolve takes for the
// identical git-common-dir query: repoRoot outside any git work tree (or a
// machine with no git on PATH) is an ordinary case for this package's own
// tests and for a bare-directory deployment, so a git error falls back to
// the naive filepath.Join(repoRoot, ".pg-router", "config.toml") rather than
// failing Load() outright. GatePaths() below reuses this same helper so its
// own documented "identical precedence" promise holds for a linked worktree
// too.
func resolveConfigPath(repoRoot string) string {
	naive := filepath.Join(repoRoot, ".pg-router", "config.toml")
	ctx := context.Background()
	client, err := gitclient.New(ctx, repoRoot)
	if err != nil {
		return naive
	}
	commonDir, err := client.CommonDir(ctx)
	if err != nil {
		return naive
	}
	return filepath.Join(filepath.Dir(commonDir), ".pg-router", "config.toml")
}

// noConfigWarnSuppressed reports whether PG_ROUTER_NO_CONFIG_WARN opts out of
// the missing-config WARN (pg2-xl659 acceptance criterion 3): the explicit
// escape hatch for a deployment that intentionally runs on built-in roles
// and does not want an ongoing operator-visible nag every Load(). Opting out
// still leaves an INFO trace (this package's pre-pg2-xl659 behavior)
// rather than going fully silent.
func noConfigWarnSuppressed() bool {
	return envBool("PG_ROUTER_NO_CONFIG_WARN", false)
}

// Validate runs the PRE-RUNTIME wiring checks and blocks on anything determinable
// as an invalid configuration. Six conditions are blocking, and they are the ones
// docs/behavior states (INV-WORKFLOW-1, USECASE-VALIDATE-CONFIG):
//
//  1. orphan event type          — a binding matches a type no source emits (a
//     wildcard emits/binds entry such as "pr.*" is rejected under this check
//     too: routing is by exact string equality, so it can never match)
//  2. unhandled source output    — a source emits a type no binding declares
//  3. disconnected handler       — a handler no binding can reach
//  4. handler with no events     — a BOUND handler whose reachable event set is empty
//  5. absent backing command     — a source's/handler's command cannot be invoked
//  6. non-terminating re-entry   — a cycle the declared graph shows cannot terminate
//
// plus each resolved query's own Validate. Errors are AGGREGATED (errors.Join),
// never early-returned, so a bad config reports every problem at once at
// pre-flight.
//
// PermissionMode is deliberately NOT checked here (docket pg2-oju6w Task 5.7):
// pg-router treats it as an opaque, un-validated string, kept only to display
// in `config --show`/`config --show --json`. The real claude --permission-mode
// enum check now lives in the new module's own config validation
// (packages/pg-router-ccpool-handler/internal/config), which is the side that
// actually invokes `ccpool new --permission-mode` and therefore needs the real
// values.
//
// EXACTLY ONE condition warns instead of blocking — a re-entry cycle whose
// termination is NOT determinable — and that category is closed at one member:
// nothing else here may warn. The warning goes to slog.Warn, the channel this
// layer already uses for pre-flight diagnostics (see cmd/pg-router's stub-query and
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
	// emitted collects every event type produced by some query; bound collects
	// every event type consumed by some role.
	//
	// The Gate Registry's own routable record types (gate.set / gate.cleared /
	// gate.expired, internal/eventqueue/gate.go) are emitted by the core itself,
	// so a role may bind them without any [[query]] declaring them.
	emitted := map[string]bool{
		eventqueue.GateEventSet:     true,
		eventqueue.GateEventCleared: true,
		eventqueue.GateEventExpired: true,
	}
	for _, s := range c.Queries {
		if s.Query == nil {
			continue
		}
		if err := s.Query.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("query %q: %w", s.Name, err))
		}
		for _, e := range s.Query.Emits() {
			emitted[e] = true
			if isWildcardEventType(e) {
				errs = append(errs, fmt.Errorf("query %q emits wildcard event type %q; routing is by exact string equality, so list each event type explicitly", s.Name, e))
			}
		}
	}
	bound := map[string]bool{}
	for _, role := range c.Roles {
		for _, b := range role.Binds {
			bound[b] = true
			if isWildcardEventType(b) {
				errs = append(errs, fmt.Errorf("role %q binds wildcard event type %q; routing is by exact string equality, so list each event type explicitly", role.Name, b))
			}
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
	errs = append(errs, c.logLimitFindings()...)
	errs = append(errs, c.absentBackingCommands()...)
	cycleErrs, cycleWarns := c.reentryCycleFindings()
	errs = append(errs, cycleErrs...)
	warns = append(warns, cycleWarns...)
	return errs, warns
}

// isWildcardEventType reports whether an emits/binds entry contains a wildcard
// character. Matching is by exact string equality (S22), so such an entry can
// never match a real event type; it is rejected rather than left to surface as
// a confusing orphan finding.
func isWildcardEventType(s string) bool { return strings.ContainsRune(s, '*') }

// logLimitFindings checks the queue-log size settings (bead pg2-5d3ui): the hard
// limit max_log_bytes must be positive, and the thresholds must be ordered
// compact_threshold_bytes < soft < max_log_bytes — otherwise a compaction could
// never run before the emitters halt, or the halt could never come before
// rejection. A compact_threshold_bytes of 0 disables runtime compaction and so
// takes no part in the ordering.
func (c Config) logLimitFindings() []error {
	if c.MaxLogBytes <= 0 {
		return []error{fmt.Errorf("max_log_bytes must be > 0, got %d (set [pool].max_log_bytes or PG_ROUTER_MAX_LOG_BYTES to a positive size, e.g. 64MiB)", c.MaxLogBytes)}
	}
	if c.CompactThresholdBytes > 0 && c.CompactThresholdBytes >= c.SoftLogBytes() {
		return []error{fmt.Errorf("log size thresholds must satisfy compact_threshold_bytes < soft < max_log_bytes, got compact_threshold_bytes=%d, soft=%d (%d%% of max_log_bytes=%d)",
			c.CompactThresholdBytes, c.SoftLogBytes(), SoftLogPercent, c.MaxLogBytes)}
	}
	return nil
}

// handlerIsDisconnected is check 3 — "a handler no binding can reach". A binding
// is not a first-class object here: it is the handler's own Binds list, so the
// only way no binding reaches a handler is for that list to be empty. (A TOML
// role cannot reach this state — buildRole requires binds — so this guards a
// role built directly in Go, e.g. by a test.)
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

// absentBackingCommands is check 5 — every configured source's backing
// command must be invocable. This is the only check that probes the
// ENVIRONMENT rather than the configuration, which is why the probe is
// injected (Config.Locator) instead of calling exec.LookPath inline. A
// participant that declares no backing command (an in-process event source)
// is skipped.
//
// As of docket pg2-oju6w's Task 5.4 (ADR 0065's "Open question resolved"
// section), a ROLE no longer declares a backing command at all — the former
// handlerBackingCommand (role.Type-driven: a command role's own argv[0], or
// the ccpool binary) had no successor once Type/CCPoolConfig/CommandConfig
// were deleted from roles.Role: which executable a registered handler
// participant runs through is now entirely that participant's own concern,
// reached over the wire (internal/wireclient), never authored in pr-pool's
// own config. So this check narrows to sources only; a handler-side
// equivalent belongs to the handler module's own config validation.
func (c Config) absentBackingCommands() []error {
	loc := c.locator()
	var errs []error
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

// LogDir resolves ONLY the log/state directory — Default() overlaid with
// PG_ROUTER_LOG_DIR — without loading, parsing or validating config.toml.
//
// It exists for the entry points that need nothing but the state directory and
// must not be able to fail on unrelated config: the core's socket + discovery
// record live under LogDir, so a manager→core callback (`ingest-event`) has to be
// able to FIND a running core even when the repo-local config.toml is missing or
// broken. Load() remains the full resolution for everything else.
func LogDir() string {
	return envStr("PG_ROUTER_LOG_DIR", Default().LogDir)
}

func stateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/.local/state"
}

// configHome resolves the XDG config base dir for the pg-router global config file,
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

// DefaultCompactThresholdBytes is Default().CompactThresholdBytes: 8 MiB. The
// log held 33 MB of mostly dead history after 15 days (about 2.2 MB/day) while its
// live state was a few KB, so this compacts roughly every few days at today's
// rate and keeps replay and restart cost small.
const DefaultCompactThresholdBytes int64 = 8 << 20

// DefaultMaxLogBytes is Default().MaxLogBytes: 64 MiB, about twice the 33 MB the
// live log reached in its first 15 days uncompacted (bead pg2-5d3ui) and roughly
// 30 days of that growth if compaction were failing — far above the few KB a
// compacted log holds.
const DefaultMaxLogBytes int64 = 64 << 20

// SoftLogPercent is the soft threshold as a percentage of MaxLogBytes (an
// arbitrary starting value, one derived constant rather than a second setting):
// above it the polled emitters are halted.
const SoftLogPercent = 90

// SoftLogBytes is the derived soft log-size threshold: SoftLogPercent of
// MaxLogBytes. Above it (after a compaction attempt) the queue halts polled
// emitters; at MaxLogBytes it rejects events outright.
func (c Config) SoftLogBytes() int64 { return c.MaxLogBytes / 100 * SoftLogPercent }

// ParseBytes parses a byte count: a plain non-negative integer ("67108864") or
// an integer with a unit suffix, binary (KiB, MiB, GiB, TiB) or decimal (KB, MB,
// GB, TB), case-insensitive, with optional space ("64 MiB"). Anything else —
// empty, negative, fractional, an unknown unit, an overflow — is an error.
func ParseBytes(s string) (int64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("%q is not a size: want an integer with an optional unit (e.g. 67108864 or 64MiB)", s)
	}
	n, err := strconv.ParseInt(t[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", s, err)
	}
	unit := strings.ToLower(strings.TrimSpace(t[i:]))
	mult := int64(1)
	switch unit {
	case "", "b":
	case "kib", "k":
		mult = 1 << 10
	case "mib", "m":
		mult = 1 << 20
	case "gib", "g":
		mult = 1 << 30
	case "tib", "t":
		mult = 1 << 40
	case "kb":
		mult = 1000
	case "mb":
		mult = 1000 * 1000
	case "gb":
		mult = 1000 * 1000 * 1000
	case "tb":
		mult = 1000 * 1000 * 1000 * 1000
	default:
		return 0, fmt.Errorf("%q: unknown unit %q (use B, KiB, MiB, GiB, TiB or KB, MB, GB, TB)", s, t[i:])
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("%q overflows", s)
	}
	return n * mult, nil
}

// envBytes overlays a byte count from env. Unlike envInt it does NOT silently
// fall back on a value it cannot parse: a mistyped size limit (e.g. "64Mib " vs
// "64 megabytes") would otherwise be ignored without a word. An unset or empty
// variable keeps def.
func envBytes(key string, def int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := ParseBytes(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
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
