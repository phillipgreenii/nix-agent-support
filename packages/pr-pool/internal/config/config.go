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
	"path/filepath"
	"strconv"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

type Config struct {
	RepoRoot       string
	BeadsPrefix    string
	WorktreeDir    string
	SkillMD        string
	WorkerSkillMD  string
	MaxFeedback    int
	MaxWorker      int
	MaxWait        time.Duration
	PollInterval   time.Duration
	QuotaPaused    string
	CICDDown       string
	Effort         string
	Model          string
	PermissionMode string
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
}

// Default returns the built-in defaults (mirrors pr-pool.sh's ${VAR:-default}).
func Default() Config {
	cwd, _ := os.Getwd()
	state := stateHome()
	return Config{
		RepoRoot:       cwd,
		BeadsPrefix:    "zr",
		WorktreeDir:    state + "/pr-pool/worktrees",
		SkillMD:        "",
		WorkerSkillMD:  "",
		MaxFeedback:    1,
		MaxWorker:      1,
		MaxWait:        1800 * time.Second,
		PollInterval:   10 * time.Second,
		QuotaPaused:    "",
		CICDDown:       "",
		Effort:         "max",
		Model:          "",
		Autonomous:     true,      // workers are human-less; AskUserQuestion is structurally blocked via ccpool --autonomous
		PermissionMode: "dontAsk", // deny-by-default: auto-DENY any tool outside AllowedTools, non-interactive. PR_POOL_PERMISSION_MODE=bypassPermissions is the opt-in escape for an attended/trusted run.
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
		c.Roles = roles.BuiltinRoleSet(roles.BuiltinParams{
			WorktreeDir:   c.WorktreeDir,
			SkillMD:       c.SkillMD,
			WorkerSkillMD: c.WorkerSkillMD,
			MaxFeedback:   c.MaxFeedback,
			MaxWorker:     c.MaxWorker,
			WorkerBudget:  c.WorkerBudget(),
		})
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

// Validate checks operator-overridable fields that would otherwise fail late:
// PermissionMode, plus each resolved role's query. Errors are aggregated so a bad
// config reports every problem at once at pre-flight.
func (c Config) Validate() error {
	var errs []error
	if !validPermissionModes[c.PermissionMode] {
		errs = append(errs, fmt.Errorf("invalid PR_POOL_PERMISSION_MODE %q (valid: default, acceptEdits, plan, auto, dontAsk, bypassPermissions)", c.PermissionMode))
	}
	for _, role := range c.Roles {
		if role.Query != nil {
			if err := role.Query.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("role %q query: %w", role.Name, err))
			}
		}
	}
	return errors.Join(errs...)
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
