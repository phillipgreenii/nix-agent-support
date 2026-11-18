package sensorcli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is a sensor's TOML configuration: the table left after the common keys
// (dwell, valid_for_s) are taken. An absent --config file means an empty Config
// (built-in defaults).
type Config struct {
	Raw map[string]any
}

// Defaults of the keys every sensor shares (strawman, all configurable).
const (
	DefaultDwell     = 60 * time.Second
	DefaultValidForS = 180
)

// loadConfig reads path (empty means no file) and splits off the common keys.
func loadConfig(path string) (Config, time.Duration, int, error) {
	raw := map[string]any{}
	if path != "" {
		if _, err := toml.DecodeFile(path, &raw); err != nil {
			return Config{}, 0, 0, fmt.Errorf("config %s: %w", path, err)
		}
	}
	dwell := DefaultDwell
	if v, ok := raw["dwell"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return Config{}, 0, 0, fmt.Errorf("config key dwell: want a duration string, got %T", v)
		}
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return Config{}, 0, 0, fmt.Errorf("config key dwell: invalid duration %q", s)
		}
		dwell = d
		delete(raw, "dwell")
	}
	valid := DefaultValidForS
	if v, ok := raw["valid_for_s"]; ok {
		n, isInt := v.(int64)
		if !isInt || n < 1 || n > 86400 {
			return Config{}, 0, 0, fmt.Errorf("config key valid_for_s: want an integer in 1..86400, got %v", v)
		}
		valid = int(n)
		delete(raw, "valid_for_s")
	}
	return Config{Raw: raw}, dwell, valid, nil
}

// CheckKeys returns an error naming every top-level key not in allowed, so a
// sensor can reject an invalid key.
func (c Config) CheckKeys(allowed ...string) error {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var bad []string
	for k := range c.Raw {
		if !ok[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("invalid config key(s): %s", strings.Join(bad, ", "))
}

// String returns a string key, or def when absent; a key of the wrong type is
// an error.
func (c Config) String(key, def string) (string, error) {
	v, ok := c.Raw[key]
	if !ok {
		return def, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return "", fmt.Errorf("config key %s: want a string, got %T", key, v)
	}
	return s, nil
}

// Duration returns a duration-string key, or def when absent.
func (c Config) Duration(key string, def time.Duration) (time.Duration, error) {
	v, ok := c.Raw[key]
	if !ok {
		return def, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return 0, fmt.Errorf("config key %s: want a duration string, got %T", key, v)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("config key %s: invalid duration %q", key, s)
	}
	return d, nil
}

// StringList returns a list-of-strings key, or nil when absent.
func (c Config) StringList(key string) ([]string, error) {
	v, ok := c.Raw[key]
	if !ok {
		return nil, nil
	}
	list, isList := v.([]any)
	if !isList {
		return nil, fmt.Errorf("config key %s: want a list of strings, got %T", key, v)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, isStr := e.(string)
		if !isStr {
			return nil, fmt.Errorf("config key %s: want a list of strings, found %T", key, e)
		}
		out = append(out, s)
	}
	return out, nil
}

// Table returns a table-of-strings key (`[trackers]`), or nil when absent.
func (c Config) Table(key string) (map[string]string, error) {
	v, ok := c.Raw[key]
	if !ok {
		return nil, nil
	}
	t, isTable := v.(map[string]any)
	if !isTable {
		return nil, fmt.Errorf("config key %s: want a table, got %T", key, v)
	}
	out := make(map[string]string, len(t))
	for k, e := range t {
		s, isStr := e.(string)
		if !isStr {
			return nil, fmt.Errorf("config key %s.%s: want a string, got %T", key, k, e)
		}
		out[k] = s
	}
	return out, nil
}
