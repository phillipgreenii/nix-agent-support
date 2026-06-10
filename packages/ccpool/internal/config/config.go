// Package config loads ccpool configuration from $XDG_CONFIG_HOME/ccpool/config.toml,
// applying defaults, and resolves the XDG data/state/runtime paths.
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

	// Resolved (not from TOML):
	DBPath     string `toml:"-"`
	StateDir   string `toml:"-"`
	RuntimeDir string `toml:"-"`
}

type Notify struct {
	Adapter string   `toml:"adapter"` // none | exec | desktop
	On      []string `toml:"on"`      // states that trigger a notification
	Command string   `toml:"command"` // argv template for adapter=exec
}

type Pool struct {
	MaxSessions int      `toml:"max_sessions"`
	IdleTTL     Duration `toml:"idle_ttl"`
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
		Pool:   Pool{MaxSessions: 6, IdleTTL: Duration(30 * time.Minute)},
		Tmux:   Tmux{Socket: "ccpool", Prefix: "cc-"},
		Claude: Claude{Bin: "claude"},
		List:   List{DoneTTL: Duration(time.Hour), FailedTTL: Duration(24 * time.Hour)},
		Wait:   Wait{Timeout: Duration(10 * time.Minute)},
		Notify: Notify{Adapter: "desktop", On: []string{"needs_input", "failed"}},
	}
}

func xdg(envVar, fallbackRel string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallbackRel)
}

// StateDirPath resolves $XDG_STATE_HOME/ccpool (or ~/.local/state/ccpool) using
// only env/home — no config.toml read — so diagnostics logging works even when
// Load() itself fails on a malformed config.
func StateDirPath() string {
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "ccpool")
}

// Load reads config.toml (if present) over the defaults and resolves paths.
func Load() (Config, error) {
	c := defaults()
	cfgHome := xdg("XDG_CONFIG_HOME", ".config")
	cfgFile := filepath.Join(cfgHome, "ccpool", "config.toml")
	if _, err := os.Stat(cfgFile); err == nil {
		if _, err := toml.DecodeFile(cfgFile, &c); err != nil {
			return Config{}, fmt.Errorf("decode %s: %w", cfgFile, err)
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("stat %s: %w", cfgFile, err)
	}

	dataHome := xdg("XDG_DATA_HOME", ".local/share")
	c.DBPath = filepath.Join(dataHome, "ccpool", "store.db")
	c.StateDir = StateDirPath()
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		c.RuntimeDir = filepath.Join(rt, "ccpool")
	} else {
		c.RuntimeDir = filepath.Join(os.TempDir(), "ccpool")
	}
	return c, nil
}

// Helpers used by later plans / convenience accessors.
func (c Config) DoneTTL() time.Duration { return time.Duration(c.List.DoneTTL) }
