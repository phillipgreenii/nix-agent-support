// Package config loads and validates the pg-assistants configuration file, a
// TOML file rendered by the home-manager module.
//
// Validation is isolated per assistant: an assistant that breaks a rule gets
// the problem in its own Problems and the daemon does not start it, while
// every other assistant runs. A global field that breaks a rule falls back to
// its default and the problem is kept in Config.Problems. Only a file that
// cannot be read or parsed, or an invalid [paths] entry, makes Load fail,
// because then no assistant could run at all. Strict joins every problem for
// `pg-assistants config check`, which the build runs, so a bad config fails
// the build before it is deployed.
//
// Whether an assistant's cwd exists is NOT checked here: the build sandbox
// cannot see it. The daemon checks it before each start.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Built-in defaults for the optional fields.
const (
	DefaultTick             = time.Minute
	DefaultGraceInterrupt   = 5 * time.Minute
	DefaultGraceKill        = 2 * time.Minute
	DefaultBlocksNudge      = 0.95
	DefaultBlocksInterrupt  = 0.98
	DefaultBlocksKill       = 1.00
	DefaultUnknownResetHold = time.Hour
	DefaultMinUptime        = time.Minute
	DefaultBackoffInitial   = time.Minute
	DefaultBackoffMax       = 30 * time.Minute
	DefaultStartTimeout     = 5 * time.Minute
	DefaultContextKill      = 1.00
)

// efforts are the accepted values of an assistant's effort (claude --effort).
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9-]+$`)
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidID reports whether id is a valid assistant id, [a-z0-9-]+. An id names
// a tmux session and becomes part of file paths, so nothing else is accepted.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// ResolvePath returns p with every symlink resolved, or p itself when it does
// not resolve (a directory that does not exist yet). Claude resolves a
// directory before it looks up its folder trust (on macOS /tmp is
// /private/tmp), so trust is read and written under this form, and two cwds
// with the same resolved form are one directory. The existence check and
// tmux keep the configured path.
func ResolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// reservedEnv are the names the launch chain sets itself: the daemon's fixed
// session entries and the CLAUDE* markers it blanks. blank_env cannot list
// them and an assistant's env cannot set them. Keep in step with
// supervisor.LaunchEnv.
var reservedEnv = []string{
	"DISABLE_AUTO_COMPACT", "PG_ASSISTANT_ID", "PG_ASSISTANTS_STATE_DIR",
	"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_EXECPATH",
}

// Config is the loaded configuration. Durations and points hold their
// effective values: a global field that failed validation holds its default
// and is listed in Problems.
type Config struct {
	Tick    time.Duration
	Grace   Grace
	Blocks  Blocks
	Restart Restart
	Paths   Paths
	// BlankEnv names the variables every session starts with set and empty
	// (blank_env): the run-once guards of the operator's startup files, which
	// the tmux server would otherwise hand every session from the daemon's own
	// login shell, and variables meant for the daemon alone.
	BlankEnv   []string
	Assistants []Assistant
	Problems   []FieldError // global fields that fell back to their defaults, and unknown global keys
}

// Grace is the [grace] table: the time-based ladder steps, for every ladder.
type Grace struct{ Interrupt, Kill time.Duration }

// Points are a meter's nudge, interrupt and kill points, as fractions;
// 0 < Nudge < Interrupt <= Kill.
type Points struct{ Nudge, Interrupt, Kill float64 }

// Blocks is the [blocks] table: points over the account's 5-hour and 7-day
// blocks, and how long to hold when a held block reports no reset time.
type Blocks struct {
	Points
	UnknownResetHold time.Duration
}

// Restart is the [restart] table.
type Restart struct{ MinUptime, BackoffInitial, BackoffMax, StartTimeout time.Duration }

// Paths is the [paths] table. Every entry is required and absolute.
type Paths struct{ Tmux, Claude, PgClaudeControl, Shell, StateDir, ClaudeJSON string }

// Assistant is one [[assistant]] entry.
type Assistant struct {
	ID, Name, Model, Effort, Cwd, Prompt string
	Enabled                              bool              // true when the key is absent
	Tick                                 time.Duration     // resolved: own value or Config.Tick
	Env                                  map[string]string // extra session environment
	AllowedTools                         []string          // extra --allowed-tools rules, after ExitToolRule
	Context                              Context
	Quota                                *Quota       // nil: no quota
	Problems                             []FieldError // non-empty: the daemon does not start this assistant
}

// Context is the [assistant.context] table.
type Context struct {
	Limit, Window int64 // Window resolved from the model table unless set
	Points
}

// Quota is the [assistant.quota] table: a rolling-window limit on what the
// assistant's working directory consumes across sessions, either tokens (the
// four usage counters summed) or dollars (per-model tokens at the built-in
// price table). Exactly one of Tokens and Dollars is set.
type Quota struct {
	Tokens  int64
	Dollars float64
	Window  time.Duration
	Points
}

// FieldError is one validation problem, naming the field it is about, such as
// `grace.kill` or `assistant[example-a].context.kill`. An assistant whose id
// is not valid is named by its position instead: `assistant[#2]`.
type FieldError struct{ Field, Msg string }

func (e FieldError) Error() string { return e.Field + ": " + e.Msg }

// Defaults returns the global defaults, with no paths and no assistants.
func Defaults() Config {
	return Config{
		Tick:  DefaultTick,
		Grace: Grace{Interrupt: DefaultGraceInterrupt, Kill: DefaultGraceKill},
		Blocks: Blocks{
			Points:           Points{Nudge: DefaultBlocksNudge, Interrupt: DefaultBlocksInterrupt, Kill: DefaultBlocksKill},
			UnknownResetHold: DefaultUnknownResetHold,
		},
		Restart: Restart{
			MinUptime:      DefaultMinUptime,
			BackoffInitial: DefaultBackoffInitial,
			BackoffMax:     DefaultBackoffMax,
			StartTimeout:   DefaultStartTimeout,
		},
	}
}

// Load returns an error only for an unreadable or unparseable file or an
// invalid [paths] entry. Every other problem is recorded in Problems.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse is Load for file contents already read, so a caller that also hashes
// the file reads it once.
func Parse(data []byte) (Config, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return fromDocument(doc)
}

// Strict joins every Problem (global and per assistant); config check uses it.
// The result is nil when there are none, and its text has one problem per line.
func (c Config) Strict() error {
	var errs []error
	for _, p := range c.Problems {
		errs = append(errs, p)
	}
	for _, a := range c.Assistants {
		for _, p := range a.Problems {
			errs = append(errs, p)
		}
	}
	return errors.Join(errs...)
}

func fromDocument(doc map[string]any) (Config, error) {
	cfg := Defaults()
	top := &table{m: doc, problems: &cfg.Problems, global: true}

	paths, err := readPaths(top)
	if err != nil {
		return Config{}, err
	}
	cfg.Paths = paths

	top.duration("tick", &cfg.Tick)

	if g := top.sub("grace"); g != nil {
		g.duration("interrupt", &cfg.Grace.Interrupt)
		g.duration("kill", &cfg.Grace.Kill)
		g.unknown("interrupt", "kill")
	}

	if b := top.sub("blocks"); b != nil {
		pts := cfg.Blocks.Points
		ok := b.float("nudge", &pts.Nudge)
		ok = b.float("interrupt", &pts.Interrupt) && ok
		ok = b.float("kill", &pts.Kill) && ok
		if ok {
			if key, msg := checkPoints(pts); key != "" {
				b.problem(key, msg+fmt.Sprintf("%s; [blocks] uses the default points %g/%g/%g",
					writtenKeys(b, key), DefaultBlocksNudge, DefaultBlocksInterrupt, DefaultBlocksKill))
				ok = false
			}
		}
		if ok {
			cfg.Blocks.Points = pts
		}
		b.duration("unknown_reset_hold", &cfg.Blocks.UnknownResetHold)
		b.unknown("nudge", "interrupt", "kill", "unknown_reset_hold")
	}

	if r := top.sub("restart"); r != nil {
		r.duration("min_uptime", &cfg.Restart.MinUptime)
		r.duration("backoff_initial", &cfg.Restart.BackoffInitial)
		r.duration("backoff_max", &cfg.Restart.BackoffMax)
		r.duration("start_timeout", &cfg.Restart.StartTimeout)
		r.unknown("min_uptime", "backoff_initial", "backoff_max", "start_timeout")
	}

	cfg.BlankEnv = readBlankEnv(top)
	cfg.Assistants = readAssistants(top, cfg.Tick)
	top.unknown("tick", "grace", "blocks", "restart", "paths", "blank_env", "assistant")
	return cfg, nil
}

// readBlankEnv reads blank_env, an array of environment variable names. A
// value that is not an array falls back to the default (none); an entry that
// is not a variable name is a problem and is left out.
func readBlankEnv(top *table) []string {
	v, ok := top.m["blank_env"]
	if !ok {
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		top.problem("blank_env", "must be an array of environment variable names, got "+describe(v)+"; no variable is blanked")
		return nil
	}
	var names []string
	for i, e := range list {
		s, isString := e.(string)
		switch {
		case !isString:
			top.problem("blank_env", fmt.Sprintf("entry %d must be a string, got %s; it is left out", i+1, describe(e)))
		case !envKeyPattern.MatchString(s):
			top.problem("blank_env", fmt.Sprintf("entry %d, %q, is not an environment variable name ([A-Za-z_][A-Za-z0-9_]*); it is left out", i+1, s))
		case s == "HOME":
			top.problem("blank_env", fmt.Sprintf("entry %d, %q, would strip the session's home directory; it is left out", i+1, s))
		case slices.Contains(reservedEnv, s):
			top.problem("blank_env", fmt.Sprintf("entry %d, %q, is set by the launch chain itself; it is left out", i+1, s))
		default:
			names = append(names, s)
		}
	}
	return names
}

// readPaths reads [paths]. Every entry is required and MUST be an absolute
// path; a failure is fatal. An unknown key is only a problem.
func readPaths(top *table) (Paths, error) {
	v, ok := top.m["paths"]
	if !ok {
		return Paths{}, FieldError{"paths", "the [paths] table is required"}
	}
	m, isTable := v.(map[string]any)
	if !isTable {
		return Paths{}, FieldError{"paths", "must be a table, got " + describe(v)}
	}
	var p Paths
	entries := []struct {
		key  string
		dst  *string
		tmux bool // reaches tmux as an argument (argv token or -e value)
	}{
		{"tmux", &p.Tmux, false},
		{"claude", &p.Claude, true},
		{"pg_claude_control", &p.PgClaudeControl, false},
		{"shell", &p.Shell, true},
		{"state_dir", &p.StateDir, true},
		{"claude_json", &p.ClaudeJSON, false},
	}
	known := make([]string, 0, len(entries))
	for _, e := range entries {
		known = append(known, e.key)
		field := "paths." + e.key
		raw, present := m[e.key]
		if !present {
			return Paths{}, FieldError{field, "required"}
		}
		s, isString := raw.(string)
		if !isString {
			return Paths{}, FieldError{field, "must be a string, got " + describe(raw)}
		}
		if !filepath.IsAbs(s) {
			return Paths{}, FieldError{field, fmt.Sprintf("%q must be an absolute path", s)}
		}
		if e.tmux && endsTmuxEscaped(s) {
			return Paths{}, FieldError{field, fmt.Sprintf("%q %s", s, tmuxEscapedMsg)}
		}
		*e.dst = s
	}
	(&table{name: "paths", m: m, problems: top.problems}).unknown(known...)
	return p, nil
}

// readAssistants reads every [[assistant]] entry, then marks duplicate ids
// and shared cwds on every assistant involved.
func readAssistants(top *table, globalTick time.Duration) []Assistant {
	v, ok := top.m["assistant"]
	if !ok {
		return nil
	}
	list, isList := v.([]map[string]any)
	if inline, ok := v.([]any); ok && !isList {
		// An inline array of tables (assistant = [{...}]) decodes as []any.
		list, isList = make([]map[string]any, 0, len(inline)), true
		for i, e := range inline {
			m, isTable := e.(map[string]any)
			if !isTable {
				top.problem("assistant", fmt.Sprintf("entry %d must be a table, got %s; no assistant runs", i+1, describe(e)))
				return nil
			}
			list = append(list, m)
		}
	}
	if !isList {
		top.problem("assistant", "must be an array of tables ([[assistant]]), got "+describe(v)+"; no assistant runs")
		return nil
	}
	out := make([]Assistant, 0, len(list))
	labels := make([]string, 0, len(list))
	for i, m := range list {
		label := fmt.Sprintf("assistant[#%d]", i+1)
		if id, isString := m["id"].(string); isString && ValidID(id) {
			label = "assistant[" + id + "]"
		}
		labels = append(labels, label)
		out = append(out, readAssistant(label, m, globalTick))
	}

	markShared(out, labels, "id", func(a Assistant) string { return a.ID },
		func(v string, others []string) string {
			return fmt.Sprintf("id %q is also used by %s; ids MUST be unique", v, strings.Join(others, ", "))
		})
	// Compared resolved, so two paths to one directory through a symlink are
	// shared too. A cwd that does not exist (always so in the build sandbox)
	// is compared as written.
	markShared(out, labels, "cwd", func(a Assistant) string {
		if !filepath.IsAbs(a.Cwd) {
			return ""
		}
		return filepath.Clean(ResolvePath(a.Cwd))
	}, func(v string, others []string) string {
		return fmt.Sprintf("cwd %q is also the cwd of %s; one assistant per directory", v, strings.Join(others, ", "))
	})
	return out
}

// markShared records a problem on field for every assistant whose key(a) is
// non-empty and shared with another assistant. The others are named by
// position and, where it differs from the assistant's own, by label.
func markShared(as []Assistant, labels []string, field string, key func(Assistant) string, msg func(v string, others []string) string) {
	byKey := map[string][]int{}
	for i, a := range as {
		if k := key(a); k != "" {
			byKey[k] = append(byKey[k], i)
		}
	}
	for i, a := range as {
		k := key(a)
		idx := byKey[k]
		if k == "" || len(idx) < 2 {
			continue
		}
		var others []string
		for _, j := range idx {
			switch {
			case j == i:
			case labels[j] == labels[i] || labels[j] == fmt.Sprintf("assistant[#%d]", j+1):
				others = append(others, fmt.Sprintf("assistant #%d", j+1))
			default:
				others = append(others, fmt.Sprintf("assistant #%d (%s)", j+1, labels[j]))
			}
		}
		as[i].Problems = append(as[i].Problems, FieldError{labels[i] + "." + field, msg(k, others)})
	}
}

func readAssistant(label string, m map[string]any, globalTick time.Duration) Assistant {
	a := Assistant{
		Enabled: true,
		Tick:    globalTick,
		Context: Context{Points: Points{Kill: DefaultContextKill}},
	}
	t := &table{name: label, m: m, problems: &a.Problems}

	if t.requiredString("id", &a.ID) && !ValidID(a.ID) {
		t.problem("id", fmt.Sprintf("%q must match [a-z0-9-]+", a.ID))
	}
	if t.requiredString("name", &a.Name) {
		t.tmuxPassable("name", a.Name)
	}
	if t.requiredString("model", &a.Model) {
		t.tmuxPassable("model", a.Model)
	}
	if t.requiredString("effort", &a.Effort) && !slices.Contains(efforts, a.Effort) {
		t.problem("effort", fmt.Sprintf("%q must be one of %s", a.Effort, strings.Join(efforts, ", ")))
	}
	if t.requiredString("cwd", &a.Cwd) {
		switch {
		case !filepath.IsAbs(a.Cwd):
			t.problem("cwd", fmt.Sprintf("%q must be an absolute path", a.Cwd))
		case strings.Contains(a.Cwd, "#"):
			// tmux expands a format in new-session -c (## and #{...}), and a
			// directory it then cannot find starts the session in $HOME.
			t.problem("cwd", fmt.Sprintf("%q must not contain #, which tmux expands as a format in the start directory", a.Cwd))
		default:
			t.tmuxPassable("cwd", a.Cwd)
		}
	}
	if t.requiredString("prompt", &a.Prompt) {
		if strings.HasPrefix(a.Prompt, "-") {
			t.problem("prompt", fmt.Sprintf("%q must not start with \"-\"", a.Prompt))
		}
		t.tmuxPassable("prompt", a.Prompt)
	}
	t.boolean("enabled", &a.Enabled)
	t.duration("tick", &a.Tick)
	a.Env = readEnv(t)
	a.AllowedTools = readAllowedTools(t)
	readContext(t, &a)
	readQuota(t, &a)
	t.unknown("id", "name", "model", "effort", "cwd", "prompt", "enabled", "tick", "env", "allowed_tools", "context", "quota")
	return a
}

// readAllowedTools reads allowed_tools: permission rules (`Read(//abs/**)`)
// the session is started with, on top of ExitToolRule. Each reaches claude as
// a --allowed-tools value, so it MUST be non-empty, MUST NOT start with "-"
// (it would read as a flag) and MUST be tmux-passable.
func readAllowedTools(t *table) []string {
	v, ok := t.m["allowed_tools"]
	if !ok {
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		t.problem("allowed_tools", "must be an array of strings, got "+describe(v))
		return nil
	}
	var rules []string
	for i, item := range list {
		key := fmt.Sprintf("allowed_tools[%d]", i)
		s, isString := item.(string)
		switch {
		case !isString:
			t.problem(key, "must be a string, got "+describe(item))
		case s == "":
			t.problem(key, "must not be empty")
		case strings.HasPrefix(s, "-"):
			t.problem(key, fmt.Sprintf("%q must not start with \"-\"", s))
		case t.tmuxPassable(key, s):
			rules = append(rules, s)
		}
	}
	return rules
}

func readEnv(t *table) map[string]string {
	e := t.sub("env")
	if e == nil {
		return nil
	}
	env := make(map[string]string, len(e.m))
	for _, k := range sortedKeys(e.m) {
		if !envKeyPattern.MatchString(k) {
			e.problem(k, fmt.Sprintf("%q is not an environment variable name ([A-Za-z_][A-Za-z0-9_]*)", k))
			continue
		}
		if slices.Contains(reservedEnv, k) {
			e.problem(k, fmt.Sprintf("%q is set by the launch chain itself and cannot be set here", k))
			continue
		}
		var s string
		if e.str(k, &s) && e.tmuxPassable(k, s) {
			env[k] = s
		}
	}
	return env
}

func readContext(t *table, a *Assistant) {
	_, present := t.m["context"]
	c := t.sub("context")
	if c == nil {
		if present {
			return // not a table: already recorded
		}
		// Absent: every required context key is missing.
		c = &table{name: t.field("context"), m: map[string]any{}, problems: t.problems}
	}
	limitOK := c.requiredInt("limit", &a.Context.Limit)
	if limitOK && a.Context.Limit <= 0 {
		c.problem("limit", fmt.Sprintf("%d must be above 0", a.Context.Limit))
		limitOK = false
	}

	pointsOK := c.requiredFloat("nudge", &a.Context.Nudge)
	pointsOK = c.requiredFloat("interrupt", &a.Context.Interrupt) && pointsOK
	pointsOK = c.float("kill", &a.Context.Kill) && pointsOK
	if pointsOK {
		if key, msg := checkPoints(a.Context.Points); key != "" {
			c.problem(key, msg)
			pointsOK = false
		}
	}

	_, explicit := c.m["window"]
	windowOK := false
	if explicit {
		if c.requiredInt("window", &a.Context.Window) {
			if a.Context.Window > 0 {
				windowOK = true
			} else {
				c.problem("window", fmt.Sprintf("%d must be above 0", a.Context.Window))
			}
		}
	} else if a.Model != "" {
		if w, known := ContextWindow(a.Model); known {
			a.Context.Window, windowOK = w, true
		} else {
			t.problem("model", fmt.Sprintf("%q is not in the context-window table; set context.window for it", a.Model))
		}
	}

	// With auto-compact off, a full session fails instead of compacting, so a
	// kill point at or past the window can never fire.
	if limitOK && pointsOK && windowOK {
		at := float64(a.Context.Limit) * a.Context.Kill
		if !(at < float64(a.Context.Window)) {
			c.problem("kill", fmt.Sprintf("limit*kill = %d*%g = %g tokens must be below the context window, %d tokens",
				a.Context.Limit, a.Context.Kill, at, a.Context.Window))
		}
	}
	c.unknown("limit", "window", "nudge", "interrupt", "kill")
}

// readQuota reads the optional [assistant.quota] table. The window and the
// nudge and interrupt points are required; kill defaults to 1.00; exactly one
// of tokens and dollars must be given.
func readQuota(t *table, a *Assistant) {
	c := t.sub("quota")
	if c == nil {
		return
	}
	q := Quota{Points: Points{Kill: DefaultContextKill}}
	_, hasTokens := c.m["tokens"]
	_, hasDollars := c.m["dollars"]
	ok := true
	switch {
	case hasTokens && hasDollars:
		c.problem("dollars", "tokens and dollars are exclusive; set one")
		ok = false
	case !hasTokens && !hasDollars:
		c.problem("tokens", "required: set tokens or dollars")
		ok = false
	case hasTokens:
		if !c.requiredInt("tokens", &q.Tokens) {
			ok = false
		} else if q.Tokens <= 0 {
			c.problem("tokens", fmt.Sprintf("%d must be above 0", q.Tokens))
			ok = false
		}
	default:
		if !c.float("dollars", &q.Dollars) {
			ok = false
		} else if !(q.Dollars > 0) {
			c.problem("dollars", fmt.Sprintf("%g must be above 0", q.Dollars))
			ok = false
		}
	}
	if _, present := c.m["window"]; !present {
		c.problem("window", "required")
		ok = false
	} else if !c.duration("window", &q.Window) {
		ok = false
	}
	pointsOK := c.requiredFloat("nudge", &q.Nudge)
	pointsOK = c.requiredFloat("interrupt", &q.Interrupt) && pointsOK
	pointsOK = c.float("kill", &q.Kill) && pointsOK
	if pointsOK {
		if key, msg := checkPoints(q.Points); key != "" {
			c.problem(key, msg)
			pointsOK = false
		}
	}
	c.unknown("tokens", "dollars", "window", "nudge", "interrupt", "kill")
	if ok && pointsOK {
		a.Quota = &q
	}
}

// endsTmuxEscaped reports whether v ends in `\;`. tmux reads an argument
// ending in ";" as the end of a command, so the launch sends a trailing ";"
// as `\;`; a value that already ends in `\;` cannot be passed on intact
// (tmux delivers `X\\;` as "X;").
func endsTmuxEscaped(v string) bool { return strings.HasSuffix(v, `\;`) }

const tmuxEscapedMsg = `must not end in \; (tmux cannot pass that on intact)`

// tmuxPassable records a problem for key when v, a value that reaches tmux as
// an argument, ends in `\;`, and reports whether it does not.
func (t *table) tmuxPassable(key, v string) bool {
	if endsTmuxEscaped(v) {
		t.problem(key, fmt.Sprintf("%q %s", v, tmuxEscapedMsg))
		return false
	}
	return true
}

// checkPoints applies 0 < nudge < interrupt <= kill and returns the key of the
// first point that breaks it, with the reason. Each comparison is written so
// that a NaN fails it.
// writtenKeys words which [blocks] keys the user wrote next to the key
// checkPoints reported, which can be a defaulted key the user never wrote.
func writtenKeys(b *table, reported string) string {
	var written []string
	for _, k := range []string{"nudge", "interrupt", "kill"} {
		if _, ok := b.m[k]; ok {
			written = append(written, k)
		}
	}
	if len(written) == 0 {
		return ""
	}
	return fmt.Sprintf(" (checked key %q, with the key(s) you wrote: %s; the others are defaults)",
		reported, strings.Join(written, ", "))
}

func checkPoints(p Points) (key, msg string) {
	switch {
	case !(p.Nudge > 0):
		return "nudge", fmt.Sprintf("%g must be above 0", p.Nudge)
	case !(p.Interrupt > p.Nudge):
		return "interrupt", fmt.Sprintf("%g must be above nudge %g", p.Interrupt, p.Nudge)
	case !(p.Kill >= p.Interrupt):
		return "kill", fmt.Sprintf("%g must be at least interrupt %g", p.Kill, p.Interrupt)
	}
	return "", ""
}

// table reads one TOML table, records problems under its name and tracks
// which keys it knows.
type table struct {
	name     string // field prefix: "" (top level), "grace", "assistant[example-a]"
	m        map[string]any
	problems *[]FieldError
	global   bool // a failing field falls back to its default (said in the message)
}

func (t *table) field(key string) string {
	if t.name == "" {
		return key
	}
	return t.name + "." + key
}

func (t *table) problem(key, msg string) {
	*t.problems = append(*t.problems, FieldError{t.field(key), msg})
}

// sub returns the sub-table key, or nil when it is absent or not a table (the
// latter is recorded as a problem).
func (t *table) sub(key string) *table {
	v, ok := t.m[key]
	if !ok {
		return nil
	}
	m, isTable := v.(map[string]any)
	if !isTable {
		t.problem(key, "must be a table, got "+describe(v))
		return nil
	}
	return &table{name: t.field(key), m: m, problems: t.problems, global: t.global}
}

// unknown records every key of the table that is not in known, sorted, so a
// misspelt key is caught instead of silently running on a default.
func (t *table) unknown(known ...string) {
	for _, k := range sortedKeys(t.m) {
		if !slices.Contains(known, k) {
			t.problem(k, "unknown key")
		}
	}
}

// duration sets *dst from a positive Go duration string and reports whether
// it did. An absent key leaves *dst alone; a bad one is recorded and leaves
// *dst holding its default.
func (t *table) duration(key string, dst *time.Duration) bool {
	v, ok := t.m[key]
	if !ok {
		return true
	}
	fallback := ""
	if t.global {
		fallback = fmt.Sprintf("; using the default %s", *dst)
	}
	s, isString := v.(string)
	if !isString {
		t.problem(key, "must be a duration string such as \"1m\", got "+describe(v)+fallback)
		return false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		t.problem(key, fmt.Sprintf("%q is not a Go duration such as \"1m\"%s", s, fallback))
		return false
	}
	if d <= 0 {
		t.problem(key, fmt.Sprintf("%q must be positive%s", s, fallback))
		return false
	}
	*dst = d
	return true
}

// str sets *dst from a string value; false (recorded) when it is not one.
func (t *table) str(key string, dst *string) bool {
	v := t.m[key]
	s, isString := v.(string)
	if !isString {
		t.problem(key, "must be a string, got "+describe(v))
		return false
	}
	*dst = s
	return true
}

// requiredString is str for a key that MUST be present and non-empty.
func (t *table) requiredString(key string, dst *string) bool {
	if _, ok := t.m[key]; !ok {
		t.problem(key, "required")
		return false
	}
	if !t.str(key, dst) {
		return false
	}
	if *dst == "" {
		t.problem(key, "must not be empty")
		return false
	}
	return true
}

func (t *table) boolean(key string, dst *bool) bool {
	v, ok := t.m[key]
	if !ok {
		return true
	}
	b, isBool := v.(bool)
	if !isBool {
		t.problem(key, "must be true or false, got "+describe(v))
		return false
	}
	*dst = b
	return true
}

// float sets *dst from a number (a TOML integer is accepted). An absent key
// leaves *dst alone.
func (t *table) float(key string, dst *float64) bool {
	v, ok := t.m[key]
	if !ok {
		return true
	}
	switch n := v.(type) {
	case float64:
		*dst = n
	case int64:
		*dst = float64(n)
	default:
		t.problem(key, "must be a number, got "+describe(v))
		return false
	}
	return true
}

func (t *table) requiredFloat(key string, dst *float64) bool {
	if _, ok := t.m[key]; !ok {
		t.problem(key, "required")
		return false
	}
	return t.float(key, dst)
}

func (t *table) requiredInt(key string, dst *int64) bool {
	v, ok := t.m[key]
	if !ok {
		t.problem(key, "required")
		return false
	}
	n, isInt := v.(int64)
	if !isInt {
		t.problem(key, "must be an integer, got "+describe(v))
		return false
	}
	*dst = n
	return true
}

// describe names a decoded TOML value's type for a problem message.
func describe(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case bool:
		return "a boolean"
	case map[string]any:
		return "a table"
	case []map[string]any:
		return "an array of tables"
	case []any:
		return "an array"
	case time.Time:
		return "a date or time"
	case nil:
		return "nothing"
	}
	return fmt.Sprintf("%T", v)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
