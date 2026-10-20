// Package config loads work-report's configuration from a YAML file.
//
// The file is $XDG_CONFIG_HOME/work-report/config.yaml (fallback
// ~/.config/work-report/config.yaml) unless an explicit path is given (the
// global --config flag). Its keys are exactly: timezone (string), sources (a
// map from backend name to an object with enable (bool) and labels (list of
// strings)), schedule (interval and window, strings), store (path, string),
// generators and reports. The decode is strict: an unknown key is an error
// naming the key. The retired kinds key is rejected first, with the
// migration text (ErrKindsRetired), whatever it holds.
//
// Load decodes generators and reports as raw YAML nodes and checks nothing
// inside them, so a mistake in those sections cannot fail pull, query or
// status; Presets (presets.go) parses and checks them and is called only by
// report, config show and config validate.
//
// This package is the only place the other work-report packages read
// configuration and the store path from; defaulting lives here and nowhere
// else.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultInterval is the pg-router period between pulls.
	DefaultInterval = "1h"
	// DefaultWindow is the range every scheduled pull overlaps.
	DefaultWindow = "48h"
	// DefaultStorePath is the store location when neither the --store flag nor
	// store.path is set. It is kept unexpanded in a loaded Config so the
	// effective configuration reads the same on every host; StorePath expands
	// it.
	DefaultStorePath = "$XDG_STATE_HOME/work-report/store.db"

	configDirName = "work-report"
	configFile    = "config.yaml"
)

// SourceCfg is one backend's settings. A backend that has no entry in
// Config.Sources is enabled with no extra labels.
type SourceCfg struct {
	Enable bool
	Labels []string
}

// Config is work-report's parsed configuration.
type Config struct {
	// Timezone is an IANA zone name; empty means the system zone.
	Timezone string
	// Sources is keyed by backend binary name; an absent key means Enable
	// true and no labels.
	Sources  map[string]SourceCfg
	Schedule struct{ Interval, Window string }
	Store    struct{ Path string }

	// generators and reports are the raw YAML nodes of those sections, kept
	// unchecked for Presets to parse.
	generators, reports yaml.Node
}

// rawConfig is the on-disk YAML shape. It is separate from Config so the
// public type carries no decoding detail, and so an absent sources.<b>.enable
// can be told apart from an explicit false.
type rawConfig struct {
	Timezone string               `yaml:"timezone"`
	Sources  map[string]rawSource `yaml:"sources"`
	Schedule struct {
		Interval string `yaml:"interval"`
		Window   string `yaml:"window"`
	} `yaml:"schedule"`
	Store struct {
		Path string `yaml:"path"`
	} `yaml:"store"`
	Generators yaml.Node `yaml:"generators"`
	Reports    yaml.Node `yaml:"reports"`
}

type rawSource struct {
	Enable *bool    `yaml:"enable"`
	Labels []string `yaml:"labels"`
}

var windowRE = regexp.MustCompile(`^[0-9]+[hd]$`)

// defaults returns the all-defaults Config.
func defaults() Config {
	var c Config
	c.Schedule.Interval = DefaultInterval
	c.Schedule.Window = DefaultWindow
	c.Store.Path = DefaultStorePath
	return c
}

// Load reads the configuration at path. An empty path selects
// $XDG_CONFIG_HOME/work-report/config.yaml (fallback
// ~/.config/work-report/config.yaml); a missing file there yields the
// all-defaults Config. A non-empty path that does not exist is an error, so a
// mistyped --config is never silently ignored.
func Load(path string) (Config, error) {
	explicit := path != ""
	if !explicit {
		path = defaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return defaults(), nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	c := defaults()
	var raw rawConfig
	// Defaults are laid down first so an explicit empty value (for example
	// `window: ""`) is told apart from an absent key and rejected below.
	raw.Schedule.Interval = c.Schedule.Interval
	raw.Schedule.Window = c.Schedule.Window
	raw.Store.Path = c.Store.Path

	if hasTopLevelKey(data, "kinds") {
		return Config{}, fmt.Errorf("config: %s: %w", path, ErrKindsRetired)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	c.Timezone = raw.Timezone
	c.Schedule.Interval = raw.Schedule.Interval
	c.Schedule.Window = raw.Schedule.Window
	c.Store.Path = raw.Store.Path
	c.generators = raw.Generators
	c.reports = raw.Reports
	if len(raw.Sources) > 0 {
		c.Sources = make(map[string]SourceCfg, len(raw.Sources))
		for name, s := range raw.Sources {
			enable := true
			if s.Enable != nil {
				enable = *s.Enable
			}
			c.Sources[name] = SourceCfg{Enable: enable, Labels: s.Labels}
		}
	}

	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return c, nil
}

// KindsMigrationText is the translation text for the retired kinds config key
// and the retired --kind flag: it tells the operator how to define the preset
// that replaces the old narrative kind.
const KindsMigrationText = "--kind was removed; run 'work-report report <preset>'. " +
	"To keep the old narrative behavior define a preset: reports.<name>.generator: narrative, " +
	"with kinds.narrative.model becoming reports.<name>.config.model and " +
	"kinds.narrative.systemPromptFile becoming reports.<name>.config.promptFile " +
	"(it now replaces only the task section; the injection guard is fixed)."

// ErrKindsRetired is the load error for a config carrying the retired kinds
// key, whatever its content. Callers recognize it with
// errors.Is(err, config.ErrKindsRetired); its text is the migration text.
var ErrKindsRetired = errors.New("the kinds key is retired: " + KindsMigrationText)

// hasTopLevelKey reports whether the YAML document's top-level mapping has the
// key. A document that is not a mapping, or does not parse, has no such key
// (the strict decode reports those).
func hasTopLevelKey(data []byte, key string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return true
		}
	}
	return false
}

// Validate checks the values that Load cannot reject structurally: the time
// zone must load, schedule.window must be <N>h or <N>d (N >= 1) so the
// rendered last-<window> range is always accepted by the range-spec resolver,
// and schedule.interval must be a positive Go duration.
func (c Config) Validate() error { return c.validate() }

func (c Config) validate() error {
	if _, err := c.Location(); err != nil {
		return err
	}
	if err := validateWindow(c.Schedule.Window); err != nil {
		return err
	}
	return validateInterval(c.Schedule.Interval)
}

func validateWindow(w string) error {
	if !windowRE.MatchString(w) {
		return fmt.Errorf("schedule.window %q is invalid: want a whole number of hours or days such as 48h or 7d (matching ^[0-9]+[hd]$)", w)
	}
	if n, err := strconv.Atoi(w[:len(w)-1]); err != nil || n < 1 {
		return fmt.Errorf("schedule.window %q is invalid: the count must be a positive integer", w)
	}
	return nil
}

func validateInterval(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("schedule.interval %q is invalid: want a positive Go duration such as 1h or 30m: %w", s, err)
	}
	if d <= 0 {
		return fmt.Errorf("schedule.interval %q is invalid: must be positive", s)
	}
	return nil
}

// ConfigPath returns the file Load("") reads.
func ConfigPath() string { return defaultConfigPath() }

func defaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, configDirName, configFile)
}

// StorePath resolves the store database path: the --store flag if non-empty,
// else c.Store.Path with environment variables expanded, else the default
// $XDG_STATE_HOME/work-report/store.db. When XDG_STATE_HOME is unset,
// $HOME/.local/state is used both for the expansion and for the default.
func StorePath(c Config, storeFlag string) string {
	if storeFlag != "" {
		return storeFlag
	}
	p := c.Store.Path
	if p == "" {
		p = DefaultStorePath
	}
	return os.Expand(p, func(name string) string {
		if name == "XDG_STATE_HOME" {
			return stateHome()
		}
		return os.Getenv(name)
	})
}

func stateHome() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return x
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state")
}

// Location returns the configured time zone; an empty Timezone is the system
// zone.
func (c Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone %q is not a known IANA zone: %w", c.Timezone, err)
	}
	return loc, nil
}

// SourceEnabled reports whether the backend is enabled; a backend with no
// entry is enabled.
func (c Config) SourceEnabled(backend string) bool {
	s, ok := c.Sources[backend]
	if !ok {
		return true
	}
	return s.Enable
}

// SourceLabels returns the extra labels attached to every entry from the
// backend; none when the backend has no entry.
func (c Config) SourceLabels(backend string) []string {
	return c.Sources[backend].Labels
}

// PGRouterQueryText renders the pg-router [[query]] stanza that runs the
// scheduled pull. It emits escalated.pg2, the event type the deployment's
// escalation-triager role already binds, so no new role binding is needed.
// Empty schedule values render as the defaults.
func (c Config) PGRouterQueryText() string {
	interval, window := c.Schedule.Interval, c.Schedule.Window
	if interval == "" {
		interval = DefaultInterval
	}
	if window == "" {
		window = DefaultWindow
	}
	return fmt.Sprintf(`[[query]]
name = "work-report-pull"
emits = ["escalated.pg2"]
type = "command"
[query.command]
argv = ["work-report", "pull", "--range", "last-%s", "--output", "pg-router"]
format = "json"
[query.trigger]
kind = "period"
every = %q
`, window, interval)
}
