// Package config loads pg-review's config.toml and merges it onto the
// built-in defaults of internal/builtin. The tool reads only this file (from
// the XDG config directory), whether Nix rendered it or a person wrote it; it
// never reads a router configuration.
//
// Load returns the EFFECTIVE configuration: built-ins merged with config.toml
// key by key. A missing config.toml is a working configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
)

// SupportedSchemaVersion is the only config.toml schema_version accepted.
const SupportedSchemaVersion = 1

// Type aliases: the effective configuration is built from the built-in types.
type (
	Limits   = builtin.Limits
	Profile  = builtin.Profile
	Ccpool   = builtin.Ccpool
	Provider = builtin.Provider
	Lens     = builtin.Lens
)

// Scope selects which sessions belong to a review and which layers load there.
// Scopes have no built-in.
type Scope struct {
	Name     string
	Prefixes []string
	Layers   []string
}

// Tracker is the work-bead tracker an Adopted ruling files into.
type Tracker struct {
	// Command is the tracker CLI, default "bd".
	Command string
	// Dir is the tracker root passed to the CLI as -C. Empty means the
	// CLI's own working directory.
	Dir string
	// DefaultLabel is the repo label used when no Repos entry matches.
	DefaultLabel string
	// Repos map the path of a finding's first target to the repo label.
	Repos []TrackerRepo
}

// TrackerRepo maps targets under Prefix to a repo label.
type TrackerRepo struct {
	Prefix string
	Label  string
}

// LabelFor derives the repo label of a finding from its targets: the first
// target that starts with a configured prefix wins (entries tried in order),
// else DefaultLabel. It returns "" when the label cannot be determined.
func (t Tracker) LabelFor(targets []string) string {
	for _, tg := range targets {
		for _, r := range t.Repos {
			if r.Prefix != "" && strings.HasPrefix(tg, r.Prefix) {
				return r.Label
			}
		}
	}
	return t.DefaultLabel
}

// Env locates the XDG directories. Tests point it at temp directories.
type Env struct {
	ConfigHome string
	StateHome  string
	CacheHome  string
}

// EnvFromOS resolves the XDG directories from the process environment with
// the standard fallbacks under $HOME.
func EnvFromOS() Env {
	home := os.Getenv("HOME")
	pick := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return filepath.Join(home, fallback)
	}
	return Env{
		ConfigHome: pick("XDG_CONFIG_HOME", ".config"),
		StateHome:  pick("XDG_STATE_HOME", ".local/state"),
		CacheHome:  pick("XDG_CACHE_HOME", ".cache"),
	}
}

// Dir is the directory holding config.toml and the prompt files.
func (e Env) Dir() string { return filepath.Join(e.ConfigHome, "pg-review") }

// Path is the location of config.toml.
func (e Env) Path() string { return filepath.Join(e.Dir(), "config.toml") }

// Effective is the configuration every other packet consumes.
type Effective struct {
	Env Env
	// Builtins is the fs.FS the loader was given for embedded assets.
	Builtins fs.FS
	// ConfigPresent is false when there was no config.toml.
	ConfigPresent bool
	Limits        Limits
	Ccpool        Ccpool
	Profiles      []Profile
	Scopes        []Scope
	Providers     []Provider
	Lenses        []Lens
	// Tracker says where Adopted work beads are filed (see docs/outbox.md).
	Tracker Tracker

	mergeProblems []Problem
}

// MaxFindings is the effective max_findings of a lens: the lower of the lens
// value and items_max_per_run (an unset lens value counts as the limit).
func (e *Effective) MaxFindings(l Lens) int {
	if l.MaxFindings <= 0 || l.MaxFindings > e.Limits.ItemsMaxPerRun {
		return e.Limits.ItemsMaxPerRun
	}
	return l.MaxFindings
}

// Profile returns the profile with the given name.
func (e *Effective) Profile(name string) (Profile, bool) {
	for _, p := range e.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Provider returns the provider with the given name.
func (e *Effective) Provider(name string) (Provider, bool) {
	for _, p := range e.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// Lens returns the lens with the given id.
func (e *Effective) Lens(id string) (Lens, bool) {
	for _, l := range e.Lenses {
		if l.ID == id {
			return l, true
		}
	}
	return Lens{}, false
}

// Scope returns the scope with the given name.
func (e *Effective) Scope(name string) (Scope, bool) {
	for _, s := range e.Scopes {
		if s.Name == name {
			return s, true
		}
	}
	return Scope{}, false
}

// ArgsStyle is the provider's args style, defaulted by provider name.
func ArgsStyle(p Provider) string {
	if p.ArgsStyle != "" {
		return p.ArgsStyle
	}
	return builtin.DefaultArgsStyle(p.Name)
}

// raw* types decode config.toml; pointers distinguish "key omitted" (keep
// the built-in) from "key set".
type rawFile struct {
	SchemaVersion *int                  `toml:"schema_version"`
	Limits        rawLimits             `toml:"limits"`
	Profile       map[string]rawProfile `toml:"profile"`
	Ccpool        rawCcpool             `toml:"ccpool"`
	Scope         []rawScope            `toml:"scope"`
	Provider      []rawProvider         `toml:"provider"`
	Lens          []rawLens             `toml:"lens"`
	Tracker       rawTracker            `toml:"tracker"`
}

type rawTracker struct {
	Command      *string          `toml:"command"`
	Dir          *string          `toml:"dir"`
	DefaultLabel *string          `toml:"default_label"`
	Repo         []rawTrackerRepo `toml:"repo"`
}

type rawTrackerRepo struct {
	Prefix string `toml:"prefix"`
	Label  string `toml:"label"`
}

type rawLimits struct {
	ItemsMaxPerRun        *int `toml:"items_max_per_run"`
	ItemsMaxPending       *int `toml:"items_max_pending"`
	EvidenceTTLDays       *int `toml:"evidence_ttl_days"`
	FieldMaxChars         *int `toml:"field_max_chars"`
	ItemsAutoDeferDays    *int `toml:"items_auto_defer_days"`
	InteractiveTTLMinutes *int `toml:"interactive_ttl_minutes"`
}

type rawProfile struct {
	Backend    *string `toml:"backend"`
	Model      *string `toml:"model"`
	Effort     *string `toml:"effort"`
	Minutes    *int    `toml:"minutes"`
	QueriesMax *int    `toml:"queries_max"`
}

type rawCcpool struct {
	PoolDir    *string   `toml:"pool_dir"`
	NeutralDir *string   `toml:"neutral_dir"`
	ReadTools  *[]string `toml:"read_tools"`
}

type rawScope struct {
	Name     string    `toml:"name"`
	Prefixes *[]string `toml:"prefixes"`
	Layers   *[]string `toml:"layers"`
}

type rawProvider struct {
	Name           string               `toml:"name"`
	Command        *[]string            `toml:"command"`
	SchemaVersion  *int                 `toml:"schema_version"`
	TimeoutSeconds *int                 `toml:"timeout_seconds"`
	Scopes         *[]string            `toml:"scopes"`
	Queries        *map[string][]string `toml:"queries"`
	ArgsStyle      *string              `toml:"args_style"`
}

type rawLens struct {
	ID          string    `toml:"id"`
	Family      *string   `toml:"family"`
	Profile     *string   `toml:"profile"`
	BasePrompt  *string   `toml:"base_prompt"`
	Prompt      *string   `toml:"prompt"`
	Providers   *[]string `toml:"providers"`
	MaxFindings *int      `toml:"max_findings"`
	Kinds       *[]string `toml:"kinds"`
}

// Load builds the effective configuration: the built-ins merged with
// config.toml under env. builtins is the fs.FS holding embedded prompt files
// (tests inject their own; production passes builtin.FS()). A missing
// config.toml is not an error. A read failure, malformed TOML or an
// unsupported schema_version is returned as an error; per-entry problems
// (duplicate ids, missing required keys) surface through Validate.
func Load(env Env, builtins fs.FS) (*Effective, error) {
	e := &Effective{
		Env:       env,
		Builtins:  builtins,
		Limits:    builtin.DefaultLimits(),
		Ccpool:    builtin.DefaultCcpool(),
		Profiles:  builtin.Profiles(),
		Providers: builtin.Providers(),
		Lenses:    builtin.Lenses(),
		Tracker:   Tracker{Command: "bd"},
	}
	e.Ccpool.NeutralDir = filepath.Join(env.StateHome, "pg-review", "analyst-cwd")

	var raw rawFile
	if _, err := toml.DecodeFile(env.Path(), &raw); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return e, nil
		}
		return nil, fmt.Errorf("%s: %w", env.Path(), err)
	}
	e.ConfigPresent = true
	if raw.SchemaVersion != nil && *raw.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf("%s: unsupported schema_version %d (only %d is supported)",
			env.Path(), *raw.SchemaVersion, SupportedSchemaVersion)
	}
	e.merge(raw)
	return e, nil
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setStr(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func setSlice(dst *[]string, src *[]string) {
	if src != nil {
		*dst = append([]string{}, (*src)...)
	}
}

func (e *Effective) problem(table, id, format string, args ...any) {
	e.mergeProblems = append(e.mergeProblems, Problem{Table: table, ID: id, Message: fmt.Sprintf(format, args...)})
}

func (e *Effective) merge(raw rawFile) {
	l := &e.Limits
	setInt(&l.ItemsMaxPerRun, raw.Limits.ItemsMaxPerRun)
	setInt(&l.ItemsMaxPending, raw.Limits.ItemsMaxPending)
	setInt(&l.EvidenceTTLDays, raw.Limits.EvidenceTTLDays)
	setInt(&l.FieldMaxChars, raw.Limits.FieldMaxChars)
	setInt(&l.ItemsAutoDeferDays, raw.Limits.ItemsAutoDeferDays)
	setInt(&l.InteractiveTTLMinutes, raw.Limits.InteractiveTTLMinutes)

	setStr(&e.Ccpool.PoolDir, raw.Ccpool.PoolDir)
	setStr(&e.Ccpool.NeutralDir, raw.Ccpool.NeutralDir)
	setSlice(&e.Ccpool.ReadTools, raw.Ccpool.ReadTools)

	setStr(&e.Tracker.Command, raw.Tracker.Command)
	setStr(&e.Tracker.Dir, raw.Tracker.Dir)
	setStr(&e.Tracker.DefaultLabel, raw.Tracker.DefaultLabel)
	for _, r := range raw.Tracker.Repo {
		if r.Prefix == "" || r.Label == "" {
			e.problem("tracker", r.Prefix, "a [[tracker.repo]] entry needs both prefix and label")
			continue
		}
		e.Tracker.Repos = append(e.Tracker.Repos, TrackerRepo(r))
	}

	names := make([]string, 0, len(raw.Profile))
	for n := range raw.Profile {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rp := raw.Profile[n]
		idx := -1
		for i, p := range e.Profiles {
			if p.Name == n {
				idx = i
			}
		}
		if idx < 0 {
			e.Profiles = append(e.Profiles, Profile{Name: n, Backend: "ccpool", QueriesMax: 10})
			idx = len(e.Profiles) - 1
		}
		p := &e.Profiles[idx]
		setStr(&p.Backend, rp.Backend)
		setStr(&p.Model, rp.Model)
		setStr(&p.Effort, rp.Effort)
		setInt(&p.Minutes, rp.Minutes)
		setInt(&p.QueriesMax, rp.QueriesMax)
	}

	seen := map[string]bool{}
	for _, rs := range raw.Scope {
		if seen[rs.Name] {
			e.problem("scope", rs.Name, "duplicate id")
			continue
		}
		seen[rs.Name] = true
		s := Scope{Name: rs.Name}
		setSlice(&s.Prefixes, rs.Prefixes)
		setSlice(&s.Layers, rs.Layers)
		e.Scopes = append(e.Scopes, s)
	}

	seen = map[string]bool{}
	for _, rp := range raw.Provider {
		if seen[rp.Name] {
			e.problem("provider", rp.Name, "duplicate id")
			continue
		}
		seen[rp.Name] = true
		idx := -1
		for i, p := range e.Providers {
			if p.Name == rp.Name {
				idx = i
			}
		}
		if idx < 0 {
			e.Providers = append(e.Providers, Provider{Name: rp.Name, SchemaVersion: 1})
			idx = len(e.Providers) - 1
		}
		p := &e.Providers[idx]
		setSlice(&p.Command, rp.Command)
		setInt(&p.SchemaVersion, rp.SchemaVersion)
		setInt(&p.TimeoutSeconds, rp.TimeoutSeconds)
		setSlice(&p.Scopes, rp.Scopes)
		setStr(&p.ArgsStyle, rp.ArgsStyle)
		if rp.Queries != nil {
			p.Queries = map[string][]string{}
			for k, v := range *rp.Queries {
				p.Queries[k] = append([]string{}, v...)
			}
		}
	}

	seen = map[string]bool{}
	for _, rl := range raw.Lens {
		if seen[rl.ID] {
			e.problem("lens", rl.ID, "duplicate id")
			continue
		}
		seen[rl.ID] = true
		idx := -1
		for i, x := range e.Lenses {
			if x.ID == rl.ID {
				idx = i
			}
		}
		if idx < 0 {
			e.Lenses = append(e.Lenses, Lens{ID: rl.ID})
			idx = len(e.Lenses) - 1
		}
		x := &e.Lenses[idx]
		setStr(&x.Family, rl.Family)
		setStr(&x.Profile, rl.Profile)
		setStr(&x.BasePrompt, rl.BasePrompt)
		setStr(&x.Prompt, rl.Prompt)
		setSlice(&x.Providers, rl.Providers)
		setInt(&x.MaxFindings, rl.MaxFindings)
		setSlice(&x.Kinds, rl.Kinds)
	}
}
