package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// narrativeConfig is the validated generator config. A text part and its
// ...File form are resolved into one string here, so the prompt assembler
// never sees which form was used.
type narrativeConfig struct {
	Model   string
	Task    string // replaces the built-in task section when non-empty
	Prepend string
	Append  string
}

// textKey describes one config key that carries prompt text and its ...File
// form.
type textKey struct {
	inline, file string
	dst          func(*narrativeConfig, string)
}

var textKeys = []textKey{
	{"prompt", "promptFile", func(c *narrativeConfig, s string) { c.Task = s }},
	{"promptPrepend", "promptPrependFile", func(c *narrativeConfig, s string) { c.Prepend = s }},
	{"promptAppend", "promptAppendFile", func(c *narrativeConfig, s string) { c.Append = s }},
}

// parseConfig validates raw (the envelope's config member; absent or null is
// an empty object) and returns the resolved config plus one problem string per
// problem found, in a stable order. The config is usable only when there are
// no problems.
//
// Rules: every key is optional; an unknown key is a problem; a key and its
// ...File form are mutually exclusive; every value is a non-empty string; a
// ...File value is an absolute path (a relative path would depend on the
// working directory, which differs between validate and generate mode) of a
// readable file with non-blank content.
func parseConfig(raw json.RawMessage) (narrativeConfig, []string) {
	var cfg narrativeConfig
	var obj map[string]json.RawMessage
	if t := bytes.TrimSpace(raw); len(t) != 0 && !bytes.Equal(t, []byte("null")) {
		if err := json.Unmarshal(t, &obj); err != nil {
			return cfg, []string{"config must be a JSON object"}
		}
	}

	known := map[string]bool{"model": true}
	for _, k := range textKeys {
		known[k.inline] = true
		known[k.file] = true
	}
	var problems []string
	var unknown []string
	for k := range obj {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		problems = append(problems, fmt.Sprintf("unknown config key %q", k))
	}

	str := func(key string) (string, bool) {
		v, ok := obj[key]
		if !ok {
			return "", false
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			problems = append(problems, fmt.Sprintf("config key %q must be a string", key))
			return "", false
		}
		if strings.TrimSpace(s) == "" {
			problems = append(problems, fmt.Sprintf("config key %q must not be empty", key))
			return "", false
		}
		return s, true
	}

	if s, ok := str("model"); ok {
		cfg.Model = s
	}
	for _, k := range textKeys {
		_, hasInline := obj[k.inline]
		_, hasFile := obj[k.file]
		if hasInline && hasFile {
			problems = append(problems, fmt.Sprintf("config keys %q and %q are mutually exclusive", k.inline, k.file))
			continue
		}
		if s, ok := str(k.inline); ok {
			k.dst(&cfg, s)
		}
		if p, ok := str(k.file); ok {
			if !filepath.IsAbs(p) {
				problems = append(problems, fmt.Sprintf("config key %q must be an absolute path, got %q", k.file, p))
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				problems = append(problems, fmt.Sprintf("config key %q: cannot read %s: %v", k.file, p, err))
				continue
			}
			if strings.TrimSpace(string(b)) == "" {
				problems = append(problems, fmt.Sprintf("config key %q: %s is empty", k.file, p))
				continue
			}
			k.dst(&cfg, string(b))
		}
	}
	return cfg, problems
}
