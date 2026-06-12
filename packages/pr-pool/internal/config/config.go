// Package config holds pr-pool's runtime configuration. The bash pr-pool used
// env-var-with-default for everything; this preserves that exactly. TOML/XDG is
// a deliberate future seam: a loader could layer a file between Default() and
// the env overlay in Load() without changing callers.
package config

import (
	"os"
	"strconv"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/budget"
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
	QuotaPaused   string
	CICDDown      string
	Effort        string
	Model         string
	Dangerous     bool
	SessionPrefix string

	// Per-role enable flags. A disabled role is skipped at discovery (no
	// dispatches). Both default true. Env: PR_POOL_FEEDBACK_ENABLED /
	// PR_POOL_WORKER_ENABLED. (Maps to feedback.enabled / worker.enabled if/when
	// the TOML loader lands — the deliberate future seam noted above.)
	FeedbackEnabled bool
	WorkerEnabled   bool

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
}

// Default returns the built-in defaults (mirrors pr-pool.sh's ${VAR:-default}).
func Default() Config {
	cwd, _ := os.Getwd()
	state := stateHome()
	return Config{
		RepoRoot:        cwd,
		BeadsPrefix:     "zr",
		WorktreeDir:     state + "/pr-pool/worktrees",
		SkillMD:         "",
		WorkerSkillMD:   "",
		MaxFeedback:     1,
		MaxWorker:       1,
		MaxWait:         1800 * time.Second,
		PollInterval:    10 * time.Second,
		QuotaPaused:     "",
		CICDDown:        "",
		Effort:          "max",
		Model:           "",
		Dangerous:       true,
		SessionPrefix:   "pr-pool-",
		FeedbackEnabled: true,
		WorkerEnabled:   true,
		BudgetTokens:    0,                // unlimited until ccpool N3
		BudgetCost:      0,                // unlimited until ccpool N3
		BudgetTime:      25 * time.Minute, // strictly < MaxWait (30m)
		ReminderPct:     0.725,
		CancelPct:       0.90,
		HardPct:         1.00,
		LogDir:          state + "/pr-pool/log",
		ReminderMsg:     "You are nearing your budget for this bead — start wrapping up: record progress with bd comment.",
		WrapUpMsg:       "Budget nearly exhausted. Stop now: commit your notes with bd comment, then finish or hand back. Do not start new work.",
	}
}

// Load returns Default() overlaid with any PR_POOL_* environment variables.
func Load() Config {
	c := Default()
	c.RepoRoot = envStr("PR_POOL_REPO_ROOT", c.RepoRoot)
	c.BeadsPrefix = envStr("PR_POOL_BEADS_PREFIX", c.BeadsPrefix)
	c.WorktreeDir = envStr("PR_POOL_WORKTREE_DIR", c.WorktreeDir)
	c.SkillMD = envStr("PR_POOL_SKILL_MD", c.SkillMD)
	c.WorkerSkillMD = envStr("PR_POOL_WORKER_SKILL_MD", c.WorkerSkillMD)
	c.MaxFeedback = envInt("PR_POOL_MAX_FEEDBACK", c.MaxFeedback)
	c.MaxWorker = envInt("PR_POOL_MAX_WORKER", c.MaxWorker)
	c.MaxWait = envSecs("PR_POOL_MAX_WAIT", c.MaxWait)
	c.PollInterval = envSecs("PR_POOL_POLL_INTERVAL", c.PollInterval)
	c.QuotaPaused = envStr("PR_POOL_QUOTA_PAUSED", c.QuotaPaused)
	c.CICDDown = envStr("PR_POOL_CICD_DOWN", c.CICDDown)
	c.Effort = envStr("PR_POOL_EFFORT", c.Effort)
	c.Model = envStr("PR_POOL_MODEL", c.Model)
	c.Dangerous = envBool("PR_POOL_DANGEROUS", c.Dangerous)
	c.SessionPrefix = envStr("PR_POOL_SESSION_PREFIX", c.SessionPrefix)
	c.FeedbackEnabled = envBool("PR_POOL_FEEDBACK_ENABLED", c.FeedbackEnabled)
	c.WorkerEnabled = envBool("PR_POOL_WORKER_ENABLED", c.WorkerEnabled)
	c.BudgetTokens = int64(envInt("PR_POOL_BUDGET_TOKENS", int(c.BudgetTokens)))
	c.BudgetCost = int64(envInt("PR_POOL_BUDGET_COST", int(c.BudgetCost)))
	c.BudgetTime = envSecs("PR_POOL_BUDGET_TIME", c.BudgetTime)
	c.LogDir = envStr("PR_POOL_LOG_DIR", c.LogDir)
	return c
}

// WorkerBudget assembles the per-worker Budget from config scalars + the default
// price table. Today one budget for all workers; future per-agent budgets are a
// different constructor, no refactor.
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

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
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

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		switch v {
		case "0", "false", "no", "":
			return false
		default:
			return true
		}
	}
	return def
}
