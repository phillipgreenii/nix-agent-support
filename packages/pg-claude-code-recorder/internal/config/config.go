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
	// StatusFresh is how long after a file's last offset change the status loop
	// still takes its size with a Lstat (status.fresh).
	StatusFresh   time.Duration
	MinFreeBytes  int64
	WaitMax       time.Duration
	WaitMaxWaiter int
	QueryTimeout  time.Duration // deadline of every read (query.timeout)
	// ReadCacheMiB and ReadMmapMiB bound the memory of each read connection:
	// the SQLite page cache and the memory map (0 maps nothing).
	ReadCacheMiB int
	ReadMmapMiB  int
	// MaxConnections is query.max_connections: the read pool's open-connection
	// bound (at least 2; 0 means unbounded).
	MaxConnections int
	FeedRetention  time.Duration
	RemoveAfter    time.Duration
	RemoveEnable   bool
	// RemoveMaxPerRun is remove.maxPerRun and RemoveMaxPerHour remove.maxPerHour:
	// the most Conversations one removal pass, and one hour, may remove.
	RemoveMaxPerRun  int
	RemoveMaxPerHour int
	// FormatHoldRate is format.holdRate: a source whose bad-line or unknown-key
	// rate over the last day exceeds it is not removed (default 1%).
	FormatHoldRate float64
	// DoctorInterval is doctor.interval: how often the daemon runs doctor itself.
	DoctorInterval time.Duration
	// DoctorBytesPerSlice is doctor.bytes_per_slice: the bytes of the copy one
	// rolling doctor slice hashes (default 256 MiB; 0 selects the old
	// synchronous doctor). DoctorSliceEvery is doctor.slice_every (default 1 m).
	// DoctorAllowSlowCycle is doctor.allow_slow_cycle: start even when the
	// projected cycle is longer than doctor.interval.
	DoctorBytesPerSlice  int64
	DoctorSliceEvery     time.Duration
	DoctorAllowSlowCycle bool
	// WALTruncateAbove is wal.truncate_above: the WAL size in bytes above which
	// the checkpoint loop may attempt a TRUNCATE checkpoint, at most once an
	// hour (default 512 MiB; 0 disables TRUNCATE).
	WALTruncateAbove int64
	MetricsAddr      string
	// MaxFilesPerSecond is sweep.max_files_per_second: the most files per second
	// the sweep and the first catch-up ingest (hint-driven ingest of live files
	// is never paced). 0 means unlimited. Default 100; a pass over N files then
	// takes at least N / rate seconds, so keep N / rate under the 600 s
	// loop-stalled pass bound.
	MaxFilesPerSecond int
	// CopyThrottle is the pause after each backfill file copy that moved bytes,
	// so first-run backfill leaves the laptop usable.
	CopyThrottle time.Duration
	// SettleAfter is settle.after: how long every file of a Conversation must
	// have been quiet before it settles (default 24 h).
	SettleAfter time.Duration
	// SettleMode is the settle loop: "scan" (the whole-store pass every SettleTick,
	// the default) or "event" (wakes on dirty and due Conversations with an hourly
	// reconciliation). SettleReconcile is the reconciliation interval (default 1 h)
	// and SettleMinWake the shortest time between two event wakes (default 5 s).
	// Go-level settings for now; the config file keys arrive with the config packet.
	SettleMode string
	// SettleTick is settle.tick: the interval of the scan-mode settle tick
	// (default 5 m). settle.after is 24 h, so the tick needs no minute resolution.
	SettleTick      time.Duration
	SettleReconcile time.Duration
	SettleMinWake   time.Duration
	// HeldRecheck is held.recheck: how often a held Conversation is checked
	// again (default 1 h).
	HeldRecheck time.Duration
	// HeldMaxAge is how long a Conversation may be held before status calls the
	// recorder unhealthy.
	HeldMaxAge time.Duration
	// ParseMaxLineBytes is parse.maxLineBytes: a longer transcript line is a bad line.
	ParseMaxLineBytes int
	// GraceEnable is process.grace: when the registry file is still present at a
	// Process's exit, read it again after GraceWindow before deciding it was
	// present (the grace-window fallback for a slow deletion; off by default:
	// pg2-8wt1uz measured the registry file deleted 0.8 to 1.9 s before the pid
	// exits on every clean path, 3 runs per path, one claude version, one machine;
	// only SIGKILL leaves it, swept by a later claude startup, not on a timer).
	GraceEnable bool
	// GraceWindow is process.graceWindow.
	GraceWindow time.Duration
	// LivenessInterval is process.livenessInterval: how often the last-seen-alive
	// time of a live Process is refreshed.
	LivenessInterval time.Duration
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
		Interval          string  `toml:"interval"`
		VerifyEvery       *string `toml:"verify_every"`
		MaxFilesPerSecond *int    `toml:"max_files_per_second"`
	} `toml:"sweep"`
	Status struct {
		Fresh string `toml:"fresh"`
	} `toml:"status"`
	Wait struct {
		Max        string `toml:"max"`
		MaxWaiters *int   `toml:"maxWaiters"`
	} `toml:"wait"`
	Query struct {
		Timeout        string `toml:"timeout"`
		ReadCacheMiB   *int   `toml:"readCacheMiB"`
		ReadMmapMiB    *int   `toml:"readMmapMiB"`
		MaxConnections *int   `toml:"max_connections"`
	} `toml:"query"`
	Feed struct {
		Retention string `toml:"retention"`
	} `toml:"feed"`
	Remove struct {
		After      string `toml:"after"`
		Enable     bool   `toml:"enable"`
		MaxPerRun  *int   `toml:"maxPerRun"`
		MaxPerHour *int   `toml:"maxPerHour"`
	} `toml:"remove"`
	Format struct {
		HoldRate *float64 `toml:"holdRate"`
	} `toml:"format"`
	Doctor struct {
		Interval       string `toml:"interval"`
		BytesPerSlice  *int64 `toml:"bytes_per_slice"`
		SliceEvery     string `toml:"slice_every"`
		AllowSlowCycle bool   `toml:"allow_slow_cycle"`
	} `toml:"doctor"`
	WAL struct {
		TruncateAbove *int64 `toml:"truncate_above"`
	} `toml:"wal"`
	Metrics struct {
		Addr string `toml:"addr"`
	} `toml:"metrics"`
	Copy struct {
		Throttle string `toml:"throttle"`
	} `toml:"copy"`
	Held struct {
		MaxAge     string `toml:"maxAge"`
		AlertAfter string `toml:"alertAfter"`
		Recheck    string `toml:"recheck"`
	} `toml:"held"`
	Settle struct {
		After     string `toml:"after"`
		Mode      string `toml:"mode"`
		Tick      string `toml:"tick"`
		Reconcile string `toml:"reconcile"`
		MinWake   string `toml:"min_wake"`
	} `toml:"settle"`
	Parse struct {
		MaxLineBytes *int `toml:"maxLineBytes"`
	} `toml:"parse"`
	Process struct {
		Grace            *bool  `toml:"grace"`
		GraceWindow      string `toml:"graceWindow"`
		LivenessInterval string `toml:"livenessInterval"`
	} `toml:"process"`
}

// DefaultSettleTick is the default scan-mode settle tick (settle.tick). The
// settle age is 24 h, so a stage change is late by at most one tick against a
// day-scale threshold; the removal gauges refresh on a 5 minute cadence of their
// own, so a shorter tick would only repeat the cheap evaluation.
const DefaultSettleTick = 5 * time.Minute

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
		ClaudeDir:           claude,
		StateDir:            state,
		SweepInterval:       15 * time.Minute,
		MaxFilesPerSecond:   100,
		StatusFresh:         2 * time.Minute,
		MinFreeBytes:        20 << 30,
		WaitMax:             60 * time.Second,
		WaitMaxWaiter:       256,
		QueryTimeout:        30 * time.Second,
		ReadCacheMiB:        64,
		ReadMmapMiB:         512,
		FeedRetention:       14 * 24 * time.Hour,
		RemoveAfter:         30 * 24 * time.Hour,
		RemoveMaxPerRun:     50,
		RemoveMaxPerHour:    200,
		FormatHoldRate:      0.01,
		DoctorInterval:      24 * time.Hour,
		DoctorBytesPerSlice: 256 << 20,
		DoctorSliceEvery:    time.Minute,
		MaxConnections:      8,
		WALTruncateAbove:    512 << 20,
		CopyThrottle:        2 * time.Millisecond,
		SettleAfter:         24 * time.Hour,
		SettleMode:          "scan",
		SettleTick:          DefaultSettleTick,
		SettleReconcile:     time.Hour,
		SettleMinWake:       5 * time.Second,
		HeldRecheck:         time.Hour,
		HeldMaxAge:          3 * 24 * time.Hour,
		ParseMaxLineBytes:   64 << 20,
		Clients:             DefaultClients(),
		GraceEnable:         false,
		GraceWindow:         time.Second,
		LivenessInterval:    5 * time.Second,
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
	if f.Parse.MaxLineBytes != nil {
		c.ParseMaxLineBytes = *f.Parse.MaxLineBytes
	}
	if f.Process.Grace != nil {
		c.GraceEnable = *f.Process.Grace
	}
	c.RemoveEnable = f.Remove.Enable
	if f.Remove.MaxPerRun != nil {
		if *f.Remove.MaxPerRun < 1 {
			return fmt.Errorf("remove.maxPerRun must be at least 1")
		}
		c.RemoveMaxPerRun = *f.Remove.MaxPerRun
	}
	if f.Remove.MaxPerHour != nil {
		if *f.Remove.MaxPerHour < 1 {
			return fmt.Errorf("remove.maxPerHour must be at least 1")
		}
		c.RemoveMaxPerHour = *f.Remove.MaxPerHour
	}
	if f.Format.HoldRate != nil {
		if *f.Format.HoldRate < 0 || *f.Format.HoldRate > 1 {
			return fmt.Errorf("format.holdRate must be between 0 and 1")
		}
		c.FormatHoldRate = *f.Format.HoldRate
	}
	if f.Query.ReadCacheMiB != nil {
		if *f.Query.ReadCacheMiB < 1 {
			return fmt.Errorf("query.readCacheMiB must be at least 1")
		}
		c.ReadCacheMiB = *f.Query.ReadCacheMiB
	}
	if f.Query.ReadMmapMiB != nil {
		if *f.Query.ReadMmapMiB < 0 {
			return fmt.Errorf("query.readMmapMiB must not be negative")
		}
		c.ReadMmapMiB = *f.Query.ReadMmapMiB
	}
	if f.Query.MaxConnections != nil {
		if n := *f.Query.MaxConnections; n != 0 && n < 2 {
			return fmt.Errorf("query.max_connections must be at least 2 (0 means unbounded)")
		}
		c.MaxConnections = *f.Query.MaxConnections
	}
	if n := f.Sweep.MaxFilesPerSecond; n != nil {
		if *n < 0 || *n > 1_000_000 {
			return fmt.Errorf("sweep.max_files_per_second must be between 0 (unlimited) and 1000000")
		}
		c.MaxFilesPerSecond = *n
	}
	c.MetricsAddr = f.Metrics.Addr
	for _, d := range []struct {
		key string
		src string
		dst *time.Duration
	}{
		{"sweep.interval", f.Sweep.Interval, &c.SweepInterval},
		{"status.fresh", f.Status.Fresh, &c.StatusFresh},
		{"wait.max", f.Wait.Max, &c.WaitMax},
		{"query.timeout", f.Query.Timeout, &c.QueryTimeout},
		{"feed.retention", f.Feed.Retention, &c.FeedRetention},
		{"remove.after", f.Remove.After, &c.RemoveAfter},
		{"doctor.interval", f.Doctor.Interval, &c.DoctorInterval},
		{"doctor.slice_every", f.Doctor.SliceEvery, &c.DoctorSliceEvery},
		{"copy.throttle", f.Copy.Throttle, &c.CopyThrottle},
		{"held.maxAge", f.Held.MaxAge, &c.HeldMaxAge},
		{"held.alertAfter", f.Held.AlertAfter, &c.HeldMaxAge},
		{"held.recheck", f.Held.Recheck, &c.HeldRecheck},
		{"settle.after", f.Settle.After, &c.SettleAfter},
		{"settle.tick", f.Settle.Tick, &c.SettleTick},
		{"settle.reconcile", f.Settle.Reconcile, &c.SettleReconcile},
		{"settle.min_wake", f.Settle.MinWake, &c.SettleMinWake},
		{"process.graceWindow", f.Process.GraceWindow, &c.GraceWindow},
		{"process.livenessInterval", f.Process.LivenessInterval, &c.LivenessInterval},
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
	if m := f.Settle.Mode; m != "" {
		if m != "scan" && m != "event" {
			return fmt.Errorf("settle.mode: %q is not scan or event", m)
		}
		c.SettleMode = m
	}
	if f.Doctor.BytesPerSlice != nil {
		if *f.Doctor.BytesPerSlice < 0 {
			return fmt.Errorf("doctor.bytes_per_slice must not be negative (0 selects the synchronous doctor)")
		}
		c.DoctorBytesPerSlice = *f.Doctor.BytesPerSlice
	}
	c.DoctorAllowSlowCycle = f.Doctor.AllowSlowCycle
	if f.WAL.TruncateAbove != nil {
		if *f.WAL.TruncateAbove < 0 {
			return fmt.Errorf("wal.truncate_above must not be negative (0 disables TRUNCATE)")
		}
		c.WALTruncateAbove = *f.WAL.TruncateAbove
	}
	if f.Sweep.VerifyEvery != nil {
		return fmt.Errorf("sweep.verify_every was retired: the daemon no longer re-ingests every file on a timer; remove the key, and run `pg-claude-code-recorder sweep --force` to verify on request")
	}
	if c.DoctorSliceEvery < time.Second {
		return fmt.Errorf("doctor.slice_every: %s is under the 1s minimum", c.DoctorSliceEvery)
	}
	if c.SettleTick < time.Second {
		return fmt.Errorf("settle.tick: %s is under the 1s minimum", c.SettleTick)
	}
	if c.SettleReconcile < time.Minute {
		return fmt.Errorf("settle.reconcile: %s is under the 1m minimum", c.SettleReconcile)
	}
	if c.SettleMinWake < time.Second {
		return fmt.Errorf("settle.min_wake: %s is under the 1s minimum", c.SettleMinWake)
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
