// Package config resolves the active pool (CCPOOL_POOL), loads the pool or
// XDG-based config.toml, and resolves the data/state/runtime path layout.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Pool   Pool   `toml:"pool"`
	Tmux   Tmux   `toml:"tmux"`
	Claude Claude `toml:"claude"`
	List   List   `toml:"list"`
	Wait   Wait   `toml:"wait"`
	Notify Notify `toml:"notify"`
	Retry  Retry  `toml:"retry"`

	// Resolved (not from TOML):
	DBPath     string `toml:"-"`
	StateDir   string `toml:"-"`
	RuntimeDir string `toml:"-"`
	PoolRoot   string `toml:"-"` // canonical pool dir; "" in default mode
}

type Notify struct {
	Adapter string   `toml:"adapter"` // none | exec | desktop
	On      []string `toml:"on"`      // states that trigger a notification
	Command string   `toml:"command"` // argv template for adapter=exec
}

type Pool struct {
	MaxSessions int      `toml:"max_sessions"`
	IdleTTL     Duration `toml:"idle_ttl"`
	// AutoReap, default true, gates ONLY the reap-all sweep: false opts this pool
	// out of the timer-driven reap entirely (idle AND over-cap), while a manual
	// `ccpool reap` still reaps it and it stays registered. Distinct from
	// idle_ttl = 0, which disables only TTL closures but still enforces the cap.
	AutoReap bool `toml:"auto_reap"`
}
type Tmux struct {
	Socket string `toml:"socket"`
	Prefix string `toml:"prefix"`
}
type Claude struct {
	PluginDir    string `toml:"plugin_dir"`
	DefaultCwd   string `toml:"default_cwd"`
	DefaultModel string `toml:"default_model"`
	Bin          string `toml:"bin"`
}
type List struct {
	DoneTTL   Duration `toml:"done_ttl"`
	FailedTTL Duration `toml:"failed_ttl"`
}
type Wait struct {
	Timeout Duration `toml:"timeout"`
}

// Retry configures the in-session transient-error retry actuated from the
// StopFailure hook (the 2026-06-16 transient-retry design). When a turn fails
// with a transient class in Classes and budget remains, ccpool waits the
// exponential backoff (BaseDelay * 2^retry_count) and re-nudges the SAME Claude
// session instead of handing the failure back as `errored`.
type Retry struct {
	// Enabled gates the whole feature; false restores hand-back-everything.
	Enabled bool `toml:"enabled"`
	// MaxAttempts caps the number of in-place retries per window.
	MaxAttempts int `toml:"max_attempts"`
	// BaseDelay is the first backoff; the nth retry waits BaseDelay * 2^(n-1).
	BaseDelay Duration `toml:"base_delay"`
	// Timeout bounds the overall retry window (measured from the first retry) so
	// a persistently-failing session hands back promptly.
	Timeout Duration `toml:"timeout"`
	// Classes are the RetryClass names retried; default = the two transient
	// classes ("transient_server", "transient_network"). ccpool never retries
	// "rate_limited" or "terminal".
	Classes []string `toml:"classes"`
}

// Duration is a TOML-decodable time.Duration ("30m", "10m", ...).
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func defaults() Config {
	return Config{
		Pool:   Pool{MaxSessions: 6, IdleTTL: Duration(30 * time.Minute), AutoReap: true},
		Tmux:   Tmux{Socket: "ccpool", Prefix: "cc-"},
		Claude: Claude{Bin: "claude"},
		List:   List{DoneTTL: Duration(time.Hour), FailedTTL: Duration(24 * time.Hour)},
		Wait:   Wait{Timeout: Duration(10 * time.Minute)},
		Notify: Notify{Adapter: "desktop", On: []string{"needs_input", "failed"}},
		Retry: Retry{
			Enabled:     true,
			MaxAttempts: 3,
			BaseDelay:   Duration(time.Second),
			Timeout:     Duration(60 * time.Second),
			Classes:     []string{"transient_server", "transient_network"},
		},
	}
}

func xdg(envVar, fallbackRel string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallbackRel)
}

// StateDirPath resolves the active pool's state dir (holding hook.log) from
// CCPOOL_POOL using only env/fs — no config.toml read — so diagnostics logging
// survives a malformed config. Default mode → $XDG_STATE_HOME/ccpool.
func StateDirPath() string {
	if pool := os.Getenv("CCPOOL_POOL"); pool != "" {
		return canonicalize(pool)
	}
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "ccpool")
}

// Load reads the active pool's config.toml (if present) over the defaults and
// resolves paths. The active pool comes from CCPOOL_POOL (set by --pool in main),
// and the pool dir is validated/created (and registered on first create).
func Load() (Config, error) {
	pc, err := ResolvePool(os.Getenv("CCPOOL_POOL"))
	if err != nil {
		return Config{}, err
	}
	return loadFrom(pc)
}

// LoadForPool loads a SPECIFIC pool's config from its root, ignoring CCPOOL_POOL and
// without validating/creating/registering the dir (reap-all's GC has already
// validated it). An empty root loads the default (XDG) pool. This is the seam
// reap-all uses to govern every registered pool in-process — no per-iteration
// os.Setenv, so a panic mid-sweep can never leave a wrong env behind.
func LoadForPool(root string) (Config, error) {
	return loadFrom(resolvePaths(root))
}

// loadFrom decodes config.toml over the defaults and stamps in the resolved paths.
func loadFrom(pc PoolContext) (Config, error) {
	c := defaults()
	if _, err := os.Stat(pc.ConfigPath); err == nil {
		if _, err := toml.DecodeFile(pc.ConfigPath, &c); err != nil {
			return Config{}, fmt.Errorf("decode %s: %w", pc.ConfigPath, err)
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("stat %s: %w", pc.ConfigPath, err)
	}
	c.DBPath = pc.DBPath
	c.StateDir = pc.StateDir
	c.RuntimeDir = pc.RuntimeDir
	c.PoolRoot = pc.Root
	if pc.Root != "" { // pool-dir mode: derived socket + constant prefix override config
		c.Tmux.Socket = pc.Socket
		c.Tmux.Prefix = pc.Prefix
	}
	return c, nil
}

// Helpers used by later plans / convenience accessors.
func (c Config) DoneTTL() time.Duration { return time.Duration(c.List.DoneTTL) }

// EventLogPath is the active pool's append-only JSONL event log
// (<state-dir>/events.jsonl), sitting beside hook.log. See internal/eventlog.
func (c Config) EventLogPath() string { return filepath.Join(c.StateDir, "events.jsonl") }

// DiagLogPath is the active pool's append-only JSONL operator-diagnostic log
// (<state-dir>/diagnostics.jsonl), sitting beside events.jsonl and replacing the
// old plain-text hook.log. See internal/diaglog; tailed into Loki by the otelcol
// filelog receiver registered in darwin/modules/ccpool.
func (c Config) DiagLogPath() string { return filepath.Join(c.StateDir, "diagnostics.jsonl") }
