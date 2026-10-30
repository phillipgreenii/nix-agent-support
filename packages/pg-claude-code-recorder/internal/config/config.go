// Package config loads the recorder's TOML configuration. The file is rendered
// by the Home Manager module at $XDG_CONFIG_HOME/pg-claude-code-recorder/
// config.toml. An option a.b is the key b in the table [a]. Durations are Go
// duration strings with a d suffix for days ("3d", "36h"). Keys that a later
// phase owns are tolerated and ignored.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Env names read by the recorder.
const (
	EnvSocket      = "PG_CLAUDE_CODE_RECORDER_SOCKET"
	EnvConfig      = "PG_CLAUDE_CODE_RECORDER_CONFIG"
	EnvMetricsAddr = "PG_CLAUDE_CODE_RECORDER_METRICS_ADDR"
	EnvPprofAddr   = "PG_CLAUDE_CODE_RECORDER_PPROF_ADDR"
	Instance       = "pg-claude-code-recorder"
	SocketName     = "recorder.sock"
)

// Config is the loaded configuration with defaults applied.
type Config struct {
	ClaudeDir       string
	StateDir        string
	ControlStateDir string // "" when no control mod is configured
	Socket          string
	LogFile         string // "" logs to standard error
	BlockList       []string
	Clients         []string // client names accepted as a metric label

	SweepInterval time.Duration
	MinFreeBytes  int64
	WaitMax       time.Duration
	WaitMaxWaiter int
	FeedRetention time.Duration
	RemoveAfter   time.Duration
	RemoveEnable  bool
	MetricsAddr   string
	// CopyThrottle is the pause after each backfill file copy that moved bytes,
	// so first-run backfill leaves the laptop usable.
	CopyThrottle time.Duration
	// HeldMaxAge is how long a Conversation may be held before status calls the
	// recorder unhealthy.
	HeldMaxAge time.Duration
}

type file struct {
	ClaudeDir       string   `toml:"claudeDir"`
	StateDir        string   `toml:"stateDir"`
	ControlStateDir string   `toml:"controlStateDir"`
	Socket          string   `toml:"socket"`
	LogFile         string   `toml:"logFile"`
	BlockList       []string `toml:"blockList"`
	Clients         []string `toml:"clients"`
	MinFreeBytes    *int64   `toml:"minFreeBytes"`
	Sweep           struct {
		Interval string `toml:"interval"`
	} `toml:"sweep"`
	Wait struct {
		Max        string `toml:"max"`
		MaxWaiters *int   `toml:"maxWaiters"`
	} `toml:"wait"`
	Feed struct {
		Retention string `toml:"retention"`
	} `toml:"feed"`
	Remove struct {
		After  string `toml:"after"`
		Enable bool   `toml:"enable"`
	} `toml:"remove"`
	Metrics struct {
		Addr string `toml:"addr"`
	} `toml:"metrics"`
	Copy struct {
		Throttle string `toml:"throttle"`
	} `toml:"copy"`
	Held struct {
		MaxAge string `toml:"maxAge"`
	} `toml:"held"`
}

// ParseDuration parses a Go duration string with an optional d (days) unit.
func ParseDuration(s string) (time.Duration, error) {
	if !strings.Contains(s, "d") {
		return time.ParseDuration(s)
	}
	i := strings.Index(s, "d")
	days, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	d := time.Duration(days * float64(24*time.Hour))
	if rest := s[i+1:]; rest != "" {
		r, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d += r
	}
	return d, nil
}

// DefaultPath returns the config file path used when none is given.
func DefaultPath(getenv func(string) string) string {
	if p := getenv(EnvConfig); p != "" {
		return p
	}
	return filepath.Join(xdg(getenv, "XDG_CONFIG_HOME", ".config"), Instance, "config.toml")
}

func home(getenv func(string) string) string {
	if h := getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func xdg(getenv func(string) string, name, fallback string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return filepath.Join(home(getenv), fallback)
}

// StateHome is $XDG_STATE_HOME/<instance>, the parent of the pprof socket.
func StateHome(getenv func(string) string) string {
	return filepath.Join(xdg(getenv, "XDG_STATE_HOME", ".local/state"), Instance)
}

// Defaults returns the configuration with no file.
func Defaults(getenv func(string) string) Config {
	claude := getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home(getenv), ".claude")
	}
	state := StateHome(getenv)
	c := Config{
		ClaudeDir:     claude,
		StateDir:      state,
		SweepInterval: 15 * time.Minute,
		MinFreeBytes:  5 << 30,
		WaitMax:       60 * time.Second,
		WaitMaxWaiter: 64,
		FeedRetention: 30 * 24 * time.Hour,
		RemoveAfter:   30 * 24 * time.Hour,
		CopyThrottle:  2 * time.Millisecond,
		HeldMaxAge:    3 * 24 * time.Hour,
		Clients:       DefaultClients(),
	}
	c.Socket = filepath.Join(c.StateDir, SocketName)
	return c
}

// DefaultClients are the client names accepted as a metric label.
func DefaultClients() []string {
	return []string{"cli", "swiftbar", "pa-monitor", "ccpool", "pg-desk", "pg-task-focus", "pg-router", "pg-claude-code-control", "skill"}
}

// Load reads path (a missing file is an error only when mustExist) over the
// defaults. The PG_CLAUDE_CODE_RECORDER_SOCKET and _METRICS_ADDR environment
// variables override the file.
func Load(path string, mustExist bool, getenv func(string) string) (Config, error) {
	c := Defaults(getenv)
	var f file
	if _, err := toml.DecodeFile(path, &f); err != nil {
		if !(errors.Is(err, fs.ErrNotExist) && !mustExist) {
			return Config{}, fmt.Errorf("config %s: %w", path, err)
		}
	} else if err := apply(&c, f); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if v := getenv(EnvSocket); v != "" {
		c.Socket = v
	}
	if v := getenv(EnvMetricsAddr); v != "" {
		c.MetricsAddr = v
	}
	return c, nil
}

func apply(c *Config, f file) error {
	if f.ClaudeDir != "" {
		c.ClaudeDir = f.ClaudeDir
	}
	if f.StateDir != "" {
		c.StateDir = f.StateDir
		c.Socket = filepath.Join(c.StateDir, SocketName)
	}
	if f.Socket != "" {
		c.Socket = f.Socket
	}
	c.ControlStateDir = f.ControlStateDir
	c.LogFile = f.LogFile
	c.BlockList = f.BlockList
	c.Clients = append(c.Clients, f.Clients...)
	if f.MinFreeBytes != nil {
		c.MinFreeBytes = *f.MinFreeBytes
	}
	if f.Wait.MaxWaiters != nil {
		c.WaitMaxWaiter = *f.Wait.MaxWaiters
	}
	c.RemoveEnable = f.Remove.Enable
	c.MetricsAddr = f.Metrics.Addr
	for _, d := range []struct {
		key string
		src string
		dst *time.Duration
	}{
		{"sweep.interval", f.Sweep.Interval, &c.SweepInterval},
		{"wait.max", f.Wait.Max, &c.WaitMax},
		{"feed.retention", f.Feed.Retention, &c.FeedRetention},
		{"remove.after", f.Remove.After, &c.RemoveAfter},
		{"copy.throttle", f.Copy.Throttle, &c.CopyThrottle},
		{"held.maxAge", f.Held.MaxAge, &c.HeldMaxAge},
	} {
		if d.src == "" {
			continue
		}
		v, err := ParseDuration(d.src)
		if err != nil {
			return fmt.Errorf("%s: %w", d.key, err)
		}
		*d.dst = v
	}
	return nil
}

// EndsDir returns <controlStateDir>/ends, or "" when no control mod is
// configured.
func (c Config) EndsDir() string {
	if c.ControlStateDir == "" {
		return ""
	}
	return filepath.Join(c.ControlStateDir, "ends")
}

// Roots returns the watched roots: the Claude directory and the ends directory.
func (c Config) Roots() []string {
	roots := []string{c.ClaudeDir}
	if e := c.EndsDir(); e != "" {
		roots = append(roots, e)
	}
	return roots
}
