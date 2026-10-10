// Package config loads, validates and resolves the pg-task-focus
// configuration: the one JSON file that describes a routine (its tasks and
// their due rules, its cycle types, its profiles) and the few settings the
// daemon itself needs.
//
// Parse checks a document against the JSON Schema first and then applies the
// rules a schema cannot state (a profile naming a task that exists, a due rule
// fitting its cadence, a name being a zone). It reports every problem at once,
// each with the JSON pointer of the value at fault. A parsed Config is
// immutable.
//
// The configuration is read at run time only. Replay of the log never
// consults it: a task's title, group, link and due rule are snapshotted in
// the log when the task is materialized, and a cycle's title when it starts.
// So a Config that no longer defines a task, a cycle type or a profile that
// history mentions is harmless; the resolution helpers fall back to the
// defaults for a cycle type that has gone.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
)

// Problem is one thing wrong with a configuration.
type Problem struct {
	// Path is the JSON pointer (RFC 6901) of the value at fault. For a key
	// that is missing or not allowed it is the pointer of that key, so it names
	// the key itself and not the object around it. It is empty only when the
	// document as a whole is at fault (it is not JSON, or not an object).
	Path string
	// Message says what is wrong, and what would fix it, in one sentence.
	Message string
}

// ValidationError is the error Parse returns for a document it rejects. It
// lists every problem found, in the order of their paths, so one edit cycle
// fixes them all. The same type reports a bad reload (CheckReload).
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "the configuration has %d problem", len(e.Problems))
	if len(e.Problems) != 1 {
		sb.WriteByte('s')
	}
	for _, p := range e.Problems {
		where := p.Path
		if where == "" {
			where = "the document"
		}
		fmt.Fprintf(&sb, "\n- %s: %s", where, p.Message)
	}
	return sb.String()
}

// Sound is one alert sound once resolved: a sound name, or no sound at all
// (INV-CONF-7). The zero value is no sound. A configured name is never empty.
// It encodes in JSON as the name, or null for no sound, so a client can tell
// "no sound" from a name.
type Sound struct{ name string }

// NoSound is the explicit choice of no sound.
var NoSound = Sound{}

// Named returns the sound called name. An empty name is no sound.
func Named(name string) Sound { return Sound{name: name} }

// Name returns the sound's name, empty for no sound.
func (s Sound) Name() string { return s.name }

// None reports whether this is no sound.
func (s Sound) None() bool { return s.name == "" }

// MarshalJSON encodes the name, or null for no sound.
func (s Sound) MarshalJSON() ([]byte, error) {
	if s.None() {
		return []byte("null"), nil
	}
	return json.Marshal(s.name)
}

// Alert is the sound and repeat settings of one cycle type once resolved.
// Each of the two sounds is a name or an explicit no sound; neither is ever
// unset.
type Alert struct {
	Sound         Sound // played once when the cycle's time is up
	ReminderSound Sound // played on every repeat; the notification repeats either way
	RepeatMinutes int   // running minutes between reminders, until the cycle stops
}

// Attention holds the thresholds the attention feed and the clients use.
type Attention struct {
	DueSoonMinutes      int // an open task due within this many minutes is "due soon"
	OvertimeHighMinutes int // overtime becomes high severity after this many minutes
	StalePauseMinutes   int // a cycle paused longer than this is an attention item
}

// Defaults is the `defaults` section with every optional field filled in.
type Defaults struct {
	CycleMinutes         int
	BoostMinutes         []int
	Profile              string
	MaxFutureSkewSeconds int
	Alert                Alert
	Attention            Attention
}

// Profile lists the ids a profile selects: the tasks by cadence and the cycle
// types.
type Profile struct {
	Name   string
	Daily  []string
	Weekly []string
	Sprint []string
	Cycles []string
}

// TaskDef is one task definition.
type TaskDef struct {
	ID      string
	Title   string
	Cadence due.Cadence
	Group   string // empty when the task has no group
	Link    string // empty when the task has no link
	Due     due.Rule
}

// AlertOverride is the alert settings a cycle type sets for itself, as
// written. A nil sound is absent and falls back (INV-CONF-7); a non-nil
// sound that is NoSound is the explicit choice of no sound, not absent. A zero
// RepeatMinutes is not set and falls back to defaults.alert.
type AlertOverride struct {
	Sound         *Sound
	ReminderSound *Sound
	RepeatMinutes int
}

// CycleDef is one cycle type.
type CycleDef struct {
	ID      string
	Title   string
	Minutes int      // the type's planned duration; 0 when it sets none
	Keys    []string // key names pre-filled in the cycle's form
	Alert   AlertOverride
}

// Config is a parsed, validated, immutable configuration.
type Config struct {
	defaults   Defaults
	alert      AlertOverride // defaults.alert as written: ReminderSound nil when none
	listenPort int
	publicURL  string
	groupRank  map[string]int
	groups     int // len(group_order), the rank of every group not listed
	groupOrder []string
	profiles   map[string]Profile
	tasks      map[string]TaskDef
	cycles     map[string]CycleDef
	digest     string
}

// Digest returns a hash of the configuration's content: the same for two
// documents that differ only in whitespace, key order or the spelling of a
// number (25 and 25.0), and different when any value differs. It is a
// lower-case hex SHA-256 of the canonical re-encoding of the document as
// written, so spelling out a default that was left out changes it.
func (c *Config) Digest() string { return c.digest }

// Defaults returns the defaults section with every optional field filled in.
// defaults.alert is returned resolved (INV-CONF-7).
func (c *Config) Defaults() Defaults {
	d := c.defaults
	d.BoostMinutes = append([]int(nil), d.BoostMinutes...)
	return d
}

// Profile returns the profile called name.
func (c *Config) Profile(name string) (Profile, bool) {
	p, ok := c.profiles[name]
	if !ok {
		return Profile{}, false
	}
	p.Daily = append([]string(nil), p.Daily...)
	p.Weekly = append([]string(nil), p.Weekly...)
	p.Sprint = append([]string(nil), p.Sprint...)
	p.Cycles = append([]string(nil), p.Cycles...)
	return p, true
}

// Task returns the task definition with the given id.
func (c *Config) Task(id string) (TaskDef, bool) {
	t, ok := c.tasks[id]
	if !ok {
		return TaskDef{}, false
	}
	t.Due = cloneRule(t.Due)
	return t, true
}

// CycleType returns the cycle type with the given id.
func (c *Config) CycleType(id string) (CycleDef, bool) {
	t, ok := c.cycles[id]
	if !ok {
		return CycleDef{}, false
	}
	t.Keys = append([]string(nil), t.Keys...)
	return t, true
}

// GroupOrder returns group_order as written, a group listed twice included.
func (c *Config) GroupOrder() []string { return append([]string(nil), c.groupOrder...) }

// Profiles lists every profile, ordered by name, as copies.
func (c *Config) Profiles() []Profile {
	out := make([]Profile, 0, len(c.profiles))
	for _, name := range sortedNames(c.profiles) {
		p, _ := c.Profile(name)
		out = append(out, p)
	}
	return out
}

// Tasks lists every task definition, ordered by id, as copies.
func (c *Config) Tasks() []TaskDef {
	out := make([]TaskDef, 0, len(c.tasks))
	for _, id := range sortedNames(c.tasks) {
		t, _ := c.Task(id)
		out = append(out, t)
	}
	return out
}

// CycleTypes lists every cycle type, ordered by id, as copies.
func (c *Config) CycleTypes() []CycleDef {
	out := make([]CycleDef, 0, len(c.cycles))
	for _, id := range sortedNames(c.cycles) {
		t, _ := c.CycleType(id)
		out = append(out, t)
	}
	return out
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ListenPort returns the port the daemon listens on.
func (c *Config) ListenPort() int { return c.listenPort }

// PublicURL returns the base URL deep links are built from, or the empty
// string when the configuration sets none.
func (c *Config) PublicURL() string { return c.publicURL }

func cloneRule(r due.Rule) due.Rule {
	if r.Weekday != nil {
		w := *r.Weekday
		r.Weekday = &w
	}
	if r.Day != nil {
		d := *r.Day
		r.Day = &d
	}
	return r
}

func digestOf(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
