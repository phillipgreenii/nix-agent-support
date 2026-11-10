package originguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that decodes from a TOML string such as "5m".
type Duration time.Duration

// UnmarshalText parses a Go duration string.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// ConfigError marks a bad config file. The CLI maps it to ExitConfig.
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return "config: " + e.Msg }

// Config is the loaded and validated configuration.
type Config struct {
	Presence PresenceConfig
	Breaker  BreakerConfig
	Events   EventsConfig
	Realms   map[string]RealmConfig
}

// PresenceConfig configures the presence reader.
type PresenceConfig struct {
	File string
	// MaxAge is the floor of the age rule. The effective limit is
	// max(MaxAge, 2 x RefreshSeconds).
	MaxAge time.Duration
	// RefreshSeconds mirrors the writer's presenceFile.refreshSeconds.
	RefreshSeconds int
}

// BreakerConfig configures the startup-outcome Circuit Breaker.
type BreakerConfig struct {
	FailureThreshold int
	OpenDuration     time.Duration
	StartupWindow    time.Duration
	EstablishedAfter time.Duration
}

// EventsConfig configures the event writer.
type EventsConfig struct {
	RetentionDays     int
	HeartbeatInterval time.Duration
}

// RealmConfig is one authentication domain.
type RealmConfig struct {
	Name        string
	MatchHost   string
	MatchUser   string
	AgentSocket string
	Login       LoginConfig
}

// LoginConfig bounds and spaces login attempts.
type LoginConfig struct {
	AttemptTimeout time.Duration
	Backoff        []time.Duration
}

// MaxBackoff is the longest backoff step.
func (l LoginConfig) MaxBackoff() time.Duration {
	var m time.Duration
	for _, b := range l.Backoff {
		m = max(m, b)
	}
	return m
}

// MaxBackoff is the longest backoff step over every realm.
func (c *Config) MaxBackoff() time.Duration {
	var m time.Duration
	for _, r := range c.Realms {
		m = max(m, r.Login.MaxBackoff())
	}
	return m
}

var realmNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidRealmName reports whether name is usable as a realm. The name becomes
// part of file names, so the set is deliberately narrow.
func ValidRealmName(name string) bool { return realmNameRE.MatchString(name) }

// Env is the environment the path rules read.
type Env struct {
	Getenv func(string) string
	Home   string
}

// OSEnv is the real process environment.
func OSEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{Getenv: os.Getenv, Home: home}
}

func (e Env) xdg(name, fallback string) string {
	if v := e.Getenv(name); v != "" && filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(e.Home, fallback)
}

// ConfigPath is ${XDG_CONFIG_HOME:-~/.config}/pg-origin-guard/config.toml.
func (e Env) ConfigPath() string {
	return filepath.Join(e.xdg("XDG_CONFIG_HOME", ".config"), "pg-origin-guard", "config.toml")
}

// StateDir is ${XDG_STATE_HOME:-~/.local/state}/pg-origin-guard.
func (e Env) StateDir() string {
	return filepath.Join(e.StateBase(), "pg-origin-guard")
}

// StateBase is ${XDG_STATE_HOME:-~/.local/state}.
func (e Env) StateBase() string { return e.xdg("XDG_STATE_HOME", ".local/state") }

// DefaultConfig is the configuration with no file: contract defaults, no realms.
func DefaultConfig(env Env) Config {
	return Config{
		Presence: PresenceConfig{
			File:           filepath.Join(env.StateBase(), "host-activity", "userpresence.json"),
			MaxAge:         60 * time.Second,
			RefreshSeconds: 30,
		},
		Breaker: BreakerConfig{
			FailureThreshold: 2,
			OpenDuration:     60 * time.Second,
			StartupWindow:    10 * time.Second,
			EstablishedAfter: time.Second,
		},
		Events: EventsConfig{RetentionDays: 14, HeartbeatInterval: 5 * time.Minute},
		Realms: map[string]RealmConfig{},
	}
}

type rawConfig struct {
	Presence struct {
		File           string   `toml:"file"`
		MaxAge         Duration `toml:"max_age"`
		RefreshSeconds int      `toml:"refresh_seconds"`
	} `toml:"presence"`
	Breaker struct {
		FailureThreshold int      `toml:"failure_threshold"`
		OpenDuration     Duration `toml:"open_duration"`
		StartupWindow    Duration `toml:"startup_window"`
		EstablishedAfter Duration `toml:"established_after"`
	} `toml:"breaker"`
	Events struct {
		RetentionDays     int      `toml:"retention_days"`
		HeartbeatInterval Duration `toml:"heartbeat_interval"`
	} `toml:"events"`
	Realm map[string]struct {
		Match struct {
			Host string `toml:"host"`
			User string `toml:"user"`
		} `toml:"match"`
		AgentSocket string `toml:"agent_socket"`
		Login       struct {
			AttemptTimeout Duration   `toml:"attempt_timeout"`
			Backoff        []Duration `toml:"backoff"`
		} `toml:"login"`
	} `toml:"realm"`
}

// Load reads and validates the config at cfgPath. A missing file yields the
// defaults with no realms. Any other problem is a *ConfigError.
func Load(cfgPath string, env Env) (Config, error) {
	cfg := DefaultConfig(env)
	data, err := os.ReadFile(cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, &ConfigError{Msg: err.Error()}
	}
	return Parse(string(data), env)
}

// Parse validates config text.
func Parse(text string, env Env) (Config, error) {
	cfg := DefaultConfig(env)
	var raw rawConfig
	md, err := toml.Decode(text, &raw)
	if err != nil {
		return Config{}, &ConfigError{Msg: err.Error()}
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return Config{}, &ConfigError{Msg: "unknown keys: " + strings.Join(keys, ", ")}
	}

	if raw.Presence.File != "" {
		cfg.Presence.File = expandHome(raw.Presence.File, env.Home)
	}
	if md.IsDefined("presence", "max_age") {
		cfg.Presence.MaxAge = raw.Presence.MaxAge.Std()
	}
	if md.IsDefined("presence", "refresh_seconds") {
		cfg.Presence.RefreshSeconds = raw.Presence.RefreshSeconds
	}
	if md.IsDefined("breaker", "failure_threshold") {
		cfg.Breaker.FailureThreshold = raw.Breaker.FailureThreshold
	}
	if md.IsDefined("breaker", "open_duration") {
		cfg.Breaker.OpenDuration = raw.Breaker.OpenDuration.Std()
	}
	if md.IsDefined("breaker", "startup_window") {
		cfg.Breaker.StartupWindow = raw.Breaker.StartupWindow.Std()
	}
	if md.IsDefined("breaker", "established_after") {
		cfg.Breaker.EstablishedAfter = raw.Breaker.EstablishedAfter.Std()
	}
	if md.IsDefined("events", "retention_days") {
		cfg.Events.RetentionDays = raw.Events.RetentionDays
	}
	if md.IsDefined("events", "heartbeat_interval") {
		cfg.Events.HeartbeatInterval = raw.Events.HeartbeatInterval.Std()
	}

	for name, r := range raw.Realm {
		rc := RealmConfig{
			Name:        name,
			MatchHost:   r.Match.Host,
			MatchUser:   r.Match.User,
			AgentSocket: expandHome(r.AgentSocket, env.Home),
			Login: LoginConfig{
				AttemptTimeout: 3 * time.Minute,
				Backoff:        []time.Duration{5 * time.Minute, 10 * time.Minute, 30 * time.Minute},
			},
		}
		if md.IsDefined("realm", name, "login", "attempt_timeout") {
			rc.Login.AttemptTimeout = r.Login.AttemptTimeout.Std()
		}
		if md.IsDefined("realm", name, "login", "backoff") {
			rc.Login.Backoff = nil
			for _, b := range r.Login.Backoff {
				rc.Login.Backoff = append(rc.Login.Backoff, b.Std())
			}
		}
		cfg.Realms[name] = rc
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func (c *Config) validate() error {
	bad := func(format string, a ...any) error {
		return &ConfigError{Msg: fmt.Sprintf(format, a...)}
	}
	if c.Presence.File == "" || !filepath.IsAbs(c.Presence.File) {
		return bad("presence.file must be an absolute path")
	}
	if c.Presence.MaxAge <= 0 {
		return bad("presence.max_age must be positive")
	}
	if c.Presence.RefreshSeconds <= 0 {
		return bad("presence.refresh_seconds must be positive")
	}
	if c.Breaker.FailureThreshold < 1 {
		return bad("breaker.failure_threshold must be at least 1")
	}
	for name, d := range map[string]time.Duration{
		"breaker.open_duration":     c.Breaker.OpenDuration,
		"breaker.startup_window":    c.Breaker.StartupWindow,
		"breaker.established_after": c.Breaker.EstablishedAfter,
		"events.heartbeat_interval": c.Events.HeartbeatInterval,
	} {
		if d <= 0 {
			return bad("%s must be positive", name)
		}
	}
	if c.Breaker.EstablishedAfter > c.Breaker.StartupWindow {
		return bad("breaker.established_after must not exceed breaker.startup_window")
	}
	if c.Events.RetentionDays < 1 {
		return bad("events.retention_days must be at least 1")
	}
	for name, r := range c.Realms {
		if !ValidRealmName(name) {
			return bad("realm name %q must match %s", name, realmNameRE)
		}
		for _, g := range []string{r.MatchHost, r.MatchUser} {
			if _, err := path.Match(g, ""); err != nil {
				return bad("realm.%s.match: bad pattern %q", name, g)
			}
		}
		if r.AgentSocket != "" && !filepath.IsAbs(r.AgentSocket) {
			return bad("realm.%s.agent_socket must be an absolute path", name)
		}
		if r.Login.AttemptTimeout <= 0 {
			return bad("realm.%s.login.attempt_timeout must be positive", name)
		}
		if len(r.Login.Backoff) == 0 {
			return bad("realm.%s.login.backoff must not be empty", name)
		}
		for _, b := range r.Login.Backoff {
			if b <= 0 {
				return bad("realm.%s.login.backoff entries must be positive", name)
			}
		}
	}
	return nil
}
