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
// resolves paths. The active pool comes from CCPOOL_POOL (set by --pool in main).
func Load() (Config, error) {
	pc, err := ResolvePool(os.Getenv("CCPOOL_POOL"))
	if err != nil {
		return Config{}, err
	}
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
