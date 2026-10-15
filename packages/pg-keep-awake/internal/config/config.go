// Package config loads the keeper's configuration file,
// ${XDG_CONFIG_HOME:-~/.config}/pg-keep-awake/config.toml.
//
// Only the keeper reads it; the anchor gets its values from argv, and clients
// and the shim MUST NOT import this package. The keeper reads it at startup
// only, and MUST NOT crash-loop on a bad file: Load implements the fallback
// chain (the file, then config.last-good.toml, then the built-in defaults) and
// hands back the error for the keeper to log and report as
// keeper.config_error. `pg-keep-awake config check` uses the strict path
// (Check) and exits 4 when the file is invalid.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// DirName is the config directory's final path element.
const DirName = "pg-keep-awake"

// FileName is the config file's name inside the config directory.
const FileName = "config.toml"

// LastGoodName is the name of the last-known-good copy the keeper maintains
// (writing it is the keeper's job, not this package's).
const LastGoodName = "config.last-good.toml"

// MaxFileSize caps how many bytes of a config file are read. A real file is a
// few hundred bytes; anything above the cap is rejected instead of being
// buffered, so a hostile or runaway file cannot exhaust memory.
const MaxFileSize = 64 << 10

// Built-in defaults (design section 17, O1 and O4; section 10 for the pcts).
const (
	DefaultHeartbeatInterval = 10 * time.Second
	DefaultWatchdogTimeout   = 10 * time.Minute
	DefaultLinger            = 30 * time.Second
	DefaultHoldPct           = 30
	DefaultReleasePct        = 20
	DefaultHysteresisPct     = 5
)

// Config is the effective keeper configuration.
type Config struct {
	// HeartbeatInterval is the keeper tick (heartbeat) period.
	HeartbeatInterval time.Duration
	// WatchdogTimeout is how long an anchor survives without a heartbeat.
	WatchdogTimeout time.Duration
	// Linger is how long the assertion is held after the last lease ends.
	Linger  time.Duration
	Battery BatteryConfig
	OTel    OTelConfig
}

// BatteryConfig is the [battery] table.
type BatteryConfig struct {
	HoldPct       int
	ReleasePct    int
	HysteresisPct int
}

// OTelConfig is the [otel] table. An empty Endpoint means no OTel; it is never
// defaulted to a hardcoded address.
type OTelConfig struct {
	Endpoint string
}

// Defaults returns the built-in configuration, used for an absent file.
func Defaults() Config {
	return Config{
		HeartbeatInterval: DefaultHeartbeatInterval,
		WatchdogTimeout:   DefaultWatchdogTimeout,
		Linger:            DefaultLinger,
		Battery: BatteryConfig{
			HoldPct:       DefaultHoldPct,
			ReleasePct:    DefaultReleasePct,
			HysteresisPct: DefaultHysteresisPct,
		},
	}
}

// Effective returns c with the runtime cap applied: a release above the hold
// is capped at it (design section 10). Parse and ParseFile keep the values as
// written so Check can report the inversion; Load returns the effective value.
func (c Config) Effective() Config {
	c.Battery.ReleasePct = min(c.Battery.ReleasePct, c.Battery.HoldPct)
	return c
}

// Resolve returns ${XDG_CONFIG_HOME:-~/.config}/pg-keep-awake from the process
// environment.
func Resolve() (string, error) {
	home, _ := os.UserHomeDir()
	return ResolveFrom(os.Getenv("XDG_CONFIG_HOME"), home)
}

// ResolveFrom is Resolve with the inputs supplied. Per the XDG spec a relative
// xdg value is invalid and ignored.
func ResolveFrom(xdgConfigHome, home string) (string, error) {
	if xdgConfigHome != "" && filepath.IsAbs(xdgConfigHome) {
		return filepath.Join(xdgConfigHome, DirName), nil
	}
	if home == "" {
		return "", errors.New("cannot resolve config directory: XDG_CONFIG_HOME unset and no home directory")
	}
	return filepath.Join(home, ".config", DirName), nil
}

// DefaultPath is the config file path: <Resolve()>/config.toml.
func DefaultPath() (string, error) {
	dir, err := Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// rawConfig mirrors the file. Pointers distinguish absent from zero.
type rawConfig struct {
	HeartbeatInterval *string `toml:"heartbeat_interval"`
	WatchdogTimeout   *string `toml:"watchdog_timeout"`
	Linger            *string `toml:"linger"`
	Battery           *struct {
		HoldPct       *int `toml:"hold_pct"`
		ReleasePct    *int `toml:"release_pct"`
		HysteresisPct *int `toml:"hysteresis_pct"`
	} `toml:"battery"`
	OTel *struct {
		Endpoint *string `toml:"endpoint"`
	} `toml:"otel"`
}

// readBounded reads path, refusing anything that is not a regular file or
// that is larger than MaxFileSize. The error wraps fs.ErrNotExist when the
// file is absent.
func readBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxFileSize)
	}
	return data, nil
}

// Parse decodes TOML text over the built-in defaults and validates each
// value. Keys not defined by the schema are an error (a typo would otherwise
// silently run on defaults). The returned Config holds the values as written;
// see Effective. On error the returned Config is Defaults().
func Parse(data []byte) (Config, error) {
	cfg, err := parse(data)
	if err != nil {
		return Defaults(), err
	}
	return cfg, nil
}

func parse(data []byte) (Config, error) {
	var raw rawConfig
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&raw)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return Config{}, fmt.Errorf("parse config: unknown key %q", undec[0].String())
	}
	cfg := Defaults()
	if err := setDuration(&cfg.HeartbeatInterval, raw.HeartbeatInterval, "heartbeat_interval", true); err != nil {
		return Config{}, err
	}
	if err := setDuration(&cfg.WatchdogTimeout, raw.WatchdogTimeout, "watchdog_timeout", true); err != nil {
		return Config{}, err
	}
	if err := setDuration(&cfg.Linger, raw.Linger, "linger", false); err != nil {
		return Config{}, err
	}
	if raw.Battery != nil {
		if err := setPct(&cfg.Battery.HoldPct, raw.Battery.HoldPct, "battery.hold_pct"); err != nil {
			return Config{}, err
		}
		if err := setPct(&cfg.Battery.ReleasePct, raw.Battery.ReleasePct, "battery.release_pct"); err != nil {
			return Config{}, err
		}
		if err := setPct(&cfg.Battery.HysteresisPct, raw.Battery.HysteresisPct, "battery.hysteresis_pct"); err != nil {
			return Config{}, err
		}
	}
	if raw.OTel != nil && raw.OTel.Endpoint != nil {
		cfg.OTel.Endpoint = *raw.OTel.Endpoint
	}
	return cfg, nil
}

// setDuration stores a parsed duration. positive demands > 0; otherwise 0 is
// allowed. Negative is always refused.
func setDuration(dst *time.Duration, src *string, key string, positive bool) error {
	if src == nil {
		return nil
	}
	d, err := time.ParseDuration(*src)
	if err != nil {
		return fmt.Errorf("config %s: %w", key, err)
	}
	if d < 0 || (positive && d == 0) {
		want := "zero or more"
		if positive {
			want = "greater than zero"
		}
		return fmt.Errorf("config %s: %q must be %s", key, *src, want)
	}
	*dst = d
	return nil
}

func setPct(dst *int, src *int, key string) error {
	if src == nil {
		return nil
	}
	if *src < 0 || *src > 100 {
		return fmt.Errorf("config %s: %d is outside 0..100", key, *src)
	}
	*dst = *src
	return nil
}

// ParseFile reads and parses one config file. An absent file is not special
// here: the error wraps fs.ErrNotExist.
func ParseFile(path string) (Config, error) {
	data, err := readBounded(path)
	if err != nil {
		return Defaults(), fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Load implements the keeper's fallback chain and never fails: it always
// returns a usable effective Config.
//
//  1. path absent: the built-in defaults and a nil error (no config is not an
//     error).
//  2. path parses: that file's values and a nil error.
//  3. path is invalid: lastGoodPath's values when it is present and valid,
//     else the built-in defaults; the second return is the path error, for the
//     keeper to log and report as keeper.config_error.
//
// An empty lastGoodPath skips the last-good step.
func Load(path, lastGoodPath string) (Config, error) {
	cfg, err := ParseFile(path)
	if err == nil {
		return cfg.Effective(), nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults().Effective(), nil
	}
	if lastGoodPath != "" {
		if good, gerr := ParseFile(lastGoodPath); gerr == nil {
			return good.Effective(), err
		}
	}
	return Defaults().Effective(), err
}

// Check is the strict validation behind `config check`. On top of ParseFile
// it rejects the two relations the home-manager module asserts:
// watchdog_timeout >= 6 * heartbeat_interval and release_pct <= hold_pct. An
// absent file is valid (it means the defaults) unless mustExist is set.
func Check(path string, mustExist bool) error {
	cfg, err := ParseFile(path)
	if err != nil {
		if !mustExist && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if cfg.WatchdogTimeout < 6*cfg.HeartbeatInterval {
		return fmt.Errorf("config watchdog_timeout %s must be at least 6 * heartbeat_interval (%s)",
			cfg.WatchdogTimeout, 6*cfg.HeartbeatInterval)
	}
	if cfg.Battery.ReleasePct > cfg.Battery.HoldPct {
		return fmt.Errorf("config battery.release_pct %d must not exceed battery.hold_pct %d",
			cfg.Battery.ReleasePct, cfg.Battery.HoldPct)
	}
	return nil
}
