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

	UsageGate UsageGate `toml:"usage_gate"`

	Telemetry Telemetry `toml:"telemetry"`

	// Resolved (not from TOML):
	DBPath     string `toml:"-"`
	StateDir   string `toml:"-"`
	RuntimeDir string `toml:"-"`
	PoolRoot   string `toml:"-"` // canonical pool dir; "" in default mode
}

// Telemetry configures what ccpool's metrics carry.
type Telemetry struct {
	// MetricLabelAllowlist is the cardinality guard for session labels on
	// METRICS: every ccpool metric record carries a `pool` attribute plus those
	// of the session's marked labels whose key is listed here, and no others
	// (non-listed labels are dropped from metrics but still appear on logs).
	// Entries are the dotted Go/OTel label keys (for example "pgrouter.role"),
	// never the underscored Prometheus form; the collector turns the dot into
	// an underscore on export (pgrouter_role). The default is pgrouter.role
	// only. Setting this key REPLACES the default; an empty list means no
	// session labels on metrics (pool only). List only bounded, low-cardinality
	// keys: never an id-like or path-like label (external_id, session_id, bead
	// ids, filesystem paths), which would make metric series unbounded.
	MetricLabelAllowlist []string `toml:"metric_label_allowlist"`
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
	// CanonicalMCPSettingsPath, when non-empty, names a settings.local.json-shaped
	// file consulted READ-ONLY before a fresh worktree's MCP servers are
	// default-denied: a server already classified there is copied into the
	// worktree's own settings.local.json instead. Empty (the default) is feature
	// off — pure default-deny, unchanged (docs/adr/0052-ccpool-mcp-consent-canonical-decisions-consultation.md).
	// This repo supplies only the mechanism; a deployment supplies the concrete
	// path at runtime (this repo is a public flake — see its own CLAUDE.md's
	// public-repository / no-employer-disclosure policy section).
	CanonicalMCPSettingsPath string `toml:"canonical_mcp_settings_path"`
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

// UsageGate configures the account usage-window gate (the 5-hour block and the
// weekly limit): while either window is at its limit, ccpool refuses to accept
// new work — `ccpool new` and `ccpool reply` exit 8, and `ccpool capacity`
// reports zero free slots plus the limit that is hit — regardless of which
// caller (a person, pg-router's handler, a script) invoked it. The reading comes
// from the co-resident monitor's `status --json` (its rate_limits object).
//
// The gate FAILS OPEN: a missing monitor binary, an unreachable daemon, or a
// reading with no usable reset instant is "unknown", never "blocked" — ccpool
// never stops working because it could not find out whether it should.
type UsageGate struct {
	// Enabled, default true, turns the gate on. false is a pure opt-out: ccpool
	// never runs the command and never refuses on account of a usage window.
	Enabled bool `toml:"enabled"`
	// Command is the monitor binary (resolved on PATH unless absolute) run as
	// `<command> status --json`. The packaged ccpool wrapper puts the packaged
	// monitor on PATH, so the default resolves without configuration.
	Command string `toml:"command"`
	// ThresholdPct is the used_percentage at or above which a window counts as
	// hit. The default 100 means "the limit is actually reached"; lower it to
	// stop accepting work earlier.
	ThresholdPct float64 `toml:"threshold_pct"`
	// Timeout bounds one monitor query, so an admission check can never hang.
	Timeout Duration `toml:"timeout"`
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
		UsageGate: UsageGate{
			Enabled:      true,
			Command:      "pa-monitor",
			ThresholdPct: 100,
			Timeout:      Duration(5 * time.Second),
		},
		Telemetry: Telemetry{MetricLabelAllowlist: []string{"pgrouter.role"}},
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
