// Package safety holds the collector's refusal rules: the allowlisted child
// environment, its verification before every call, and the sandbox.
package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Policy names the only locations a child may be pointed at.
type Policy struct {
	// Scratch is the resolved (symlink-free) scratch directory.
	Scratch string
	// StateHome, RuntimeDir, TmpDir are the scratch XDG_STATE_HOME,
	// XDG_RUNTIME_DIR and TMPDIR.
	StateHome, RuntimeDir, TmpDir string
	// BinDir is the scratch bin directory PATH must start with.
	BinDir string
	// DeskConfig and PRConfig are the scratch config copies.
	DeskConfig, PRConfig string
	// BeadsDir is the configured READ-ONLY beads workspace ("" in hermetic bd
	// mode, where neither BEADS_DIR nor PG_CONNECTOR_ISSUE_BEADS_DIR may be set).
	BeadsDir string
	// Home is the real home directory children keep (the login keychain is
	// resolved through it).
	Home string
	// LiveRoots are directories the run must never use as state: the live
	// state home and everything under it.
	LiveRoots []string
}

// allowed is the environment allowlist (env -i plus these).
var allowed = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "XDG_STATE_HOME": true,
	"XDG_RUNTIME_DIR": true, "PG_DESK_CONFIG": true, "PG_PR_CONFIG": true,
	"BEADS_DIR": true, "PG_CONNECTOR_ISSUE_BEADS_DIR": true,
	"BEADS_DOLT_AUTO_START": true, "GH_PROMPT_DISABLED": true,
	"GH_NO_UPDATE_NOTIFIER": true, "NO_COLOR": true,
}

// ChildEnv builds the complete child environment: nothing is inherited.
func ChildEnv(p Policy, systemPath string) []string {
	env := []string{
		"PATH=" + p.BinDir + string(os.PathListSeparator) + systemPath,
		"HOME=" + p.Home,
		"TMPDIR=" + p.TmpDir,
		"XDG_STATE_HOME=" + p.StateHome,
		"XDG_RUNTIME_DIR=" + p.RuntimeDir,
		"PG_DESK_CONFIG=" + p.DeskConfig,
		"PG_PR_CONFIG=" + p.PRConfig,
		"BEADS_DOLT_AUTO_START=0",
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"NO_COLOR=1",
	}
	if p.BeadsDir != "" {
		env = append(env, "BEADS_DIR="+p.BeadsDir, "PG_CONNECTOR_ISSUE_BEADS_DIR="+p.BeadsDir)
	}
	return env
}

// Verify refuses an environment that differs from the policy. The error names
// the offending variable. It is called at start-up and before every tick.
func Verify(env []string, p Policy) error {
	m := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("safety: malformed environment entry %q", kv)
		}
		m[k] = v
	}
	var names []string
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !allowed[k] {
			return fmt.Errorf("safety: environment variable %s is not on the allowlist", k)
		}
	}
	if err := verifyState(m["XDG_STATE_HOME"], "XDG_STATE_HOME", p); err != nil {
		return err
	}
	for _, c := range []struct{ name, got, want string }{
		{"PG_DESK_CONFIG", m["PG_DESK_CONFIG"], p.DeskConfig},
		{"PG_PR_CONFIG", m["PG_PR_CONFIG"], p.PRConfig},
		{"XDG_STATE_HOME", m["XDG_STATE_HOME"], p.StateHome},
		{"XDG_RUNTIME_DIR", m["XDG_RUNTIME_DIR"], p.RuntimeDir},
		{"TMPDIR", m["TMPDIR"], p.TmpDir},
	} {
		if c.got == "" {
			return fmt.Errorf("safety: %s is unset", c.name)
		}
		if resolve(c.got) != resolve(c.want) {
			return fmt.Errorf("safety: %s=%s differs from the configured scratch value %s", c.name, c.got, c.want)
		}
		if !Under(c.got, p.Scratch) {
			return fmt.Errorf("safety: %s=%s is not under the scratch directory %s", c.name, c.got, p.Scratch)
		}
	}
	for _, name := range []string{"BEADS_DIR", "PG_CONNECTOR_ISSUE_BEADS_DIR"} {
		got, set := m[name]
		switch {
		case p.BeadsDir == "" && set:
			return fmt.Errorf("safety: %s is set but no beads directory is configured (hermetic bd mode)", name)
		case p.BeadsDir != "" && (!set || resolve(got) != resolve(p.BeadsDir)):
			return fmt.Errorf("safety: %s=%q differs from the configured read-only beads directory %s", name, got, p.BeadsDir)
		}
	}
	if err := verifyPRConfigBeadsDirs(p); err != nil {
		return err
	}
	if m["BEADS_DOLT_AUTO_START"] != "0" {
		return fmt.Errorf("safety: BEADS_DOLT_AUTO_START must be 0 (got %q): no dolt server may be started", m["BEADS_DOLT_AUTO_START"])
	}
	if v, ok := m["PG_CONNECTOR_PR_GITHUB_EVENTS_FILE"]; ok {
		return fmt.Errorf("safety: PG_CONNECTOR_PR_GITHUB_EVENTS_FILE=%s must not be passed", v)
	}
	path := m["PATH"]
	first, _, _ := strings.Cut(path, string(os.PathListSeparator))
	if resolve(first) != resolve(p.BinDir) {
		return fmt.Errorf("safety: PATH must start with the scratch bin directory %s (got %q)", p.BinDir, first)
	}
	if m["HOME"] != p.Home {
		return fmt.Errorf("safety: HOME=%q differs from %q", m["HOME"], p.Home)
	}
	return nil
}

// BeadsDirFlag is the flag a pg-connector issue-beads instance command carries
// to name its tracker. The flag beats BEADS_DIR and PG_CONNECTOR_ISSUE_BEADS_DIR,
// so a baked-in one defeats the sandbox's read-only pin (pg2-ghmw0).
const BeadsDirFlag = "--beads-dir"

// BeadsDirWord inspects words[i] as a --beads-dir flag. It returns the
// directory it names and valueIdx, the index of the word holding the value
// (i for the `--beads-dir=<dir>` form, i+1 for the split form). ok is false
// when words[i] is not the flag; valueIdx is -1 when the split form has no
// value word.
func BeadsDirWord(words []any, i int) (dir string, valueIdx int, ok bool) {
	w, _ := words[i].(string)
	if v, found := strings.CutPrefix(w, BeadsDirFlag+"="); found {
		return v, i, true
	}
	if w != BeadsDirFlag {
		return "", 0, false
	}
	if i+1 < len(words) {
		if v, isStr := words[i+1].(string); isStr {
			return v, i + 1, true
		}
	}
	return "", -1, true
}

// verifyPRConfigBeadsDirs refuses a scratch pg-pr config whose registered
// commands carry a --beads-dir other than the policy directory. With no policy
// beads directory (hermetic bd mode) the flag may only name a path under the
// scratch directory.
func verifyPRConfigBeadsDirs(p Policy) error {
	raw, err := os.ReadFile(p.PRConfig)
	if err != nil {
		return fmt.Errorf("safety: PG_PR_CONFIG %s cannot be read to check its --beads-dir words: %w", p.PRConfig, err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("safety: PG_PR_CONFIG %s is not valid YAML: %w", p.PRConfig, err)
	}
	var bad error
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case []any:
			for i, e := range t {
				if dir, _, ok := BeadsDirWord(t, i); ok && bad == nil {
					bad = checkBeadsDir(dir, p)
				}
				walk(e)
			}
		}
	}
	walk(doc)
	return bad
}

func checkBeadsDir(dir string, p Policy) error {
	switch {
	case dir == "":
		return fmt.Errorf("safety: PG_PR_CONFIG %s has a %s with no directory", p.PRConfig, BeadsDirFlag)
	case p.BeadsDir != "" && resolve(dir) != resolve(p.BeadsDir):
		return fmt.Errorf("safety: PG_PR_CONFIG %s bakes in %s %s, which differs from the configured read-only beads directory %s", p.PRConfig, BeadsDirFlag, dir, p.BeadsDir)
	case p.BeadsDir == "" && !Under(dir, p.Scratch):
		return fmt.Errorf("safety: PG_PR_CONFIG %s bakes in %s %s, which is not under the scratch directory %s (hermetic bd mode)", p.PRConfig, BeadsDirFlag, dir, p.Scratch)
	}
	return nil
}

func verifyState(v, name string, p Policy) error {
	if v == "" {
		return fmt.Errorf("safety: %s is unset: the tools would use the live state directory", name)
	}
	if !filepath.IsAbs(v) {
		return fmt.Errorf("safety: %s=%q is not absolute", name, v)
	}
	for _, live := range p.LiveRoots {
		if live != "" && (Under(v, live) || Under(live, v)) {
			return fmt.Errorf("safety: %s=%s is (or contains) the live state directory %s", name, v, live)
		}
	}
	return nil
}

// Under reports whether path is dir or below it, after symlink resolution.
func Under(path, dir string) bool {
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolve cleans p and resolves symlinks through its deepest existing
// ancestor, so a path that does not exist yet still compares correctly.
func resolve(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolve(parent), filepath.Base(p))
}

// Resolve is the exported form of resolve.
func Resolve(p string) string { return resolve(p) }
