// Package config holds pr-pool's runtime configuration. The bash pr-pool used
// env-var-with-default for everything; this preserves that exactly. TOML/XDG is
// a deliberate future seam: a loader could layer a file between Default() and
// the env overlay in Load() without changing callers.
package config

import (
	"os"
	"strconv"
	"time"
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
		QuotaPaused:   "",
		CICDDown:      "",
		Effort:        "max",
		Model:         "",
		Dangerous:     true,
		SessionPrefix: "pr-pool-",
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
	return c
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
