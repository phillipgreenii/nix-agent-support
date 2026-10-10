package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/jsonstrict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/schemacheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// schema is the compiled configuration schema, compiled once.
var schema = sync.OnceValues(func() (*schemacheck.Schema, error) {
	return schemacheck.Compile("config.schema.json", schemas.Config())
})

// Parse reads a configuration document. It validates the document against the
// JSON Schema and then applies the semantic rules the schema cannot state, and
// reports every problem of both at once as a *ValidationError. A document that
// passes is returned as an immutable *Config.
//
// The semantic rules: a profile lists only tasks and cycle types that exist, and
// each task under its own cadence and once; a due rule carries exactly the
// fields its cadence takes, and its zone is a zone name (D12); defaults.profile
// is a defined profile; no pre-filled key is the reserved key cycle_type; and
// public_url is an http or https URL.
//
// A key that an object writes twice is refused before any of that, as the one
// problem Parse then reports: encoding/json would keep the last value and drop
// the first without a word, so a task defined twice would lose a definition.
// An optional top-level "$schema" string, the hint an editor reads, is
// accepted and ignored: it is not returned and does not change the digest.
func Parse(raw []byte) (*Config, error) {
	s, err := schema()
	if err != nil {
		return nil, fmt.Errorf("config: the embedded schema does not compile: %w", err)
	}
	p := &parser{rules: map[string]due.Rule{}}

	var violations *schemacheck.ValidationError
	if err := s.Validate(raw); err != nil {
		if !errors.As(err, &violations) {
			return nil, whole("the configuration is not valid JSON: " + strings.TrimPrefix(err.Error(), "document is not valid JSON: "))
		}
	}

	if err := jsonstrict.CheckNoDuplicateKeys(raw); err != nil {
		var dup *jsonstrict.DuplicateKeyError
		if !errors.As(err, &dup) {
			return nil, whole("the configuration is not valid JSON: " + err.Error())
		}
		return nil, &ValidationError{Problems: []Problem{{
			Path: dup.Pointer + "/" + escapePointer(dup.Key),
			Message: fmt.Sprintf("the key %q is written more than once in this object; "+
				"keep one, since only one value can apply", dup.Key),
		}}}
	}

	tree, err := decode(raw)
	if err != nil {
		return nil, whole("the configuration is not valid JSON: " + err.Error())
	}
	p.tree = tree
	if violations != nil {
		p.fromSchema(violations)
	}
	if root, ok := tree.(map[string]any); ok {
		// The editor hint is not a setting: whatever it says, it is not part
		// of the configuration, so it is not in the digest. The schema has
		// already refused one that is not a string.
		delete(root, "$schema")
		p.root = root
		p.semantic()
	}
	if len(p.problems) > 0 {
		return nil, p.failure()
	}

	canonical, err := json.Marshal(tree)
	if err != nil {
		return nil, whole("the configuration cannot be re-encoded: " + err.Error())
	}
	c := p.build()
	c.digest = digestOf(canonical)
	return c, nil
}

func whole(message string) *ValidationError {
	return &ValidationError{Problems: []Problem{{Message: message}}}
}

// decode reads the one JSON value of raw with numbers kept exact, and
// normalizes them: a number that is a whole number, however spelled (25, 25.0,
// 2.5e1), becomes the integer, so the loader and the digest see one value.
func decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("there is data after the configuration")
	}
	return normalize(v), nil
}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
	case json.Number:
		return normalizeNumber(x)
	}
	return v
}

func normalizeNumber(n json.Number) any {
	if i, err := strconv.Atoi(n.String()); err == nil {
		return i
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok || !r.IsInt() {
		return n
	}
	digits := r.Num().String()
	if i, err := strconv.Atoi(digits); err == nil {
		return i
	}
	return json.Number(digits)
}

// parser collects the problems of one document.
type parser struct {
	tree     any
	root     map[string]any
	problems []Problem
	rules    map[string]due.Rule // the due rule of each task id, once checked
}

func (p *parser) add(path, format string, args ...any) {
	p.problems = append(p.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (p *parser) reported(path string) bool {
	for _, q := range p.problems {
		if q.Path == path {
			return true
		}
	}
	return false
}

// addUnlessReported adds a problem for a value the schema has already
// complained about, only when it has not.
func (p *parser) addUnlessReported(path, format string, args ...any) {
	if !p.reported(path) {
		p.add(path, format, args...)
	}
}

// replace swaps whatever was said about path for a more specific message.
func (p *parser) replace(path, format string, args ...any) {
	kept := p.problems[:0]
	for _, q := range p.problems {
		if q.Path != path {
			kept = append(kept, q)
		}
	}
	p.problems = kept
	p.add(path, format, args...)
}

// failure returns the problems in the order of their paths, without repeats.
func (p *parser) failure() *ValidationError {
	sort.SliceStable(p.problems, func(i, j int) bool { return p.problems[i].Path < p.problems[j].Path })
	out := make([]Problem, 0, len(p.problems))
	for _, q := range p.problems {
		if len(out) > 0 && out[len(out)-1] == q {
			continue
		}
		out = append(out, q)
	}
	return &ValidationError{Problems: out}
}

// ---- the schema's violations, in an operator's words ----

// fromSchema turns the schema's violations into problems. A key that is
// missing or not allowed is reported at the key's own pointer, and its name is
// the one the violation carries, not a word of its message.
func (p *parser) fromSchema(verr *schemacheck.ValidationError) {
	for _, v := range verr.Violations {
		switch {
		case len(v.Missing) > 0:
			for _, n := range v.Missing {
				p.add(v.Pointer+"/"+escapePointer(n), "required key %q is missing", n)
			}
		case len(v.Unexpected) > 0:
			for _, n := range v.Unexpected {
				p.add(v.Pointer+"/"+escapePointer(n), "%s", unknownKey(v.Pointer, n))
			}
		default:
			p.add(v.Pointer, "%s", describe(p.tree, v))
		}
	}
}

// optionsByDesign are keys an operator might expect that the product
// deliberately does not have: the overtime sound cannot be muted, snoozed,
// acknowledged or capped at run time (a configured no sound is not one), no task carries over, and there is no day start
// (INV-CONF-8, the operator's rulings of 2026-10-07).
var optionsByDesign = []string{"snooze", "mute", "acknowledge", "max_repeat", "carry_over", "carryover"}

func unknownKey(parent, key string) string {
	switch parent {
	case "/tasks", "/cycles", "/profiles":
		if key == "" {
			return "an id must not be empty"
		}
		return fmt.Sprintf("%q is not a valid id", key)
	}
	msg := fmt.Sprintf("unknown key %q: it is not a configuration option", key)
	lower := strings.ToLower(key)
	for _, frag := range optionsByDesign {
		if strings.Contains(lower, frag) {
			return msg + ", and there is no snooze, mute, acknowledge or carry-over option by design"
		}
	}
	if strings.Contains(lower, "day_start") {
		return msg + ", and there is no day start: a day is the civil date in the zone of its due rules"
	}
	return msg
}

// valueRules word the schema's range, type and pattern violations by the
// field they concern. The message names the value as written.
var valueRules = []struct {
	path    *regexp.Regexp
	message string // %s is the value
}{
	{
		regexp.MustCompile(`^/(defaults/(cycle_minutes|boost_minutes/\d+|alert/repeat_minutes|attention/[a-z_]+)|cycles/[^/]+/(minutes|alert/repeat_minutes))$`),
		"must be a whole number of minutes from 1 to 525600 (one year); got %s",
	},
	{regexp.MustCompile(`^/defaults/max_future_skew_seconds$`), "must be a whole number of seconds from 0 to 31536000 (one year); got %s"},
	{regexp.MustCompile(`^/listen_port$`), "must be a whole number from 1 to 65535; got %s"},
	{regexp.MustCompile(`^/public_url$`), "public_url %s must be an http or https URL such as https://focus.example.test"},
	{regexp.MustCompile(`^/tasks/[^/]+/due/at$`), "due time %s is not a 24-hour HH:MM time (two digits each, 00:00 to 23:59), such as 09:00"},
	{regexp.MustCompile(`^/tasks/[^/]+/due/weekday$`), "weekday %s is not one of mon, tue, wed, thu, fri, sat, sun"},
	{regexp.MustCompile(`^/tasks/[^/]+/due/day$`), "day %s must be a whole number of at least 1 (1 is the first date of the sprint)"},
	{regexp.MustCompile(`^/tasks/[^/]+/cadence$`), "cadence %s is not one of daily, weekly, sprint"},
	{regexp.MustCompile(`^/cycles/[^/]+/keys/\d+$`), "key name %s must match [a-z0-9_-]+ (lower-case letters, digits, underscore and hyphen)"},
}

var typeMismatch = regexp.MustCompile(`^got (\w+), want (\w+)$`)

// describe words one leaf violation of the schema.
func describe(root any, v schemacheck.Violation) string {
	val, found := lookup(root, v.Pointer)
	if found {
		for _, r := range valueRules {
			if r.path.MatchString(v.Pointer) {
				return fmt.Sprintf(r.message, show(val))
			}
		}
		if s, ok := val.(string); ok && s == "" {
			return "must not be empty"
		}
		if m, ok := val.(map[string]any); ok && v.Pointer == "/profiles" && len(m) == 0 {
			return "at least one profile must be defined"
		}
	}
	if m := typeMismatch.FindStringSubmatch(v.Message); m != nil {
		return fmt.Sprintf("has the wrong type: expected %s, found %s", m[2], m[1])
	}
	return v.Message
}

// lookup follows a JSON pointer into the decoded document.
func lookup(root any, pointer string) (any, bool) {
	cur := root
	if pointer == "" {
		return cur, true
	}
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if s := string(b); len(s) <= 60 {
		return s
	}
	return string(b[:57]) + "..."
}

func escapePointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

func pointer(tokens ...string) string {
	var sb strings.Builder
	for _, t := range tokens {
		sb.WriteByte('/')
		sb.WriteString(escapePointer(t))
	}
	return sb.String()
}

// ---- the semantic rules ----

func asMap(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }
func asList(v any) ([]any, bool)         { l, ok := v.([]any); return l, ok }
func asString(v any) (string, bool)      { s, ok := v.(string); return s, ok }
func asInt(v any) (int, bool)            { i, ok := v.(int); return i, ok }

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func names(m map[string]any) string {
	if len(m) == 0 {
		return "none"
	}
	return strings.Join(sortedKeys(m), ", ")
}

var cadences = []string{string(due.Daily), string(due.Weekly), string(due.Sprint)}

// semantic applies the rules a schema cannot state. It reads the document
// leniently: a value of the wrong shape is the schema's complaint, and is
// skipped here, so one fault is not reported twice.
func (p *parser) semantic() {
	profiles, profilesOK := asMap(p.root["profiles"])
	tasks, tasksOK := asMap(p.root["tasks"])
	cycles, cyclesOK := asMap(p.root["cycles"])

	if defaults, ok := asMap(p.root["defaults"]); ok && profilesOK {
		if name, ok := asString(defaults["profile"]); ok && name != "" {
			if _, defined := profiles[name]; !defined {
				if len(profiles) == 0 {
					p.add("/defaults/profile", "defaults.profile %q is not a defined profile; no profile is defined", name)
				} else {
					p.add("/defaults/profile", "defaults.profile %q is not a defined profile; the defined profiles are %s", name, names(profiles))
				}
			}
		}
	}

	if profilesOK {
		for _, name := range sortedKeys(profiles) {
			if prof, ok := asMap(profiles[name]); ok {
				p.profile(name, prof, tasks, tasksOK, cycles, cyclesOK)
			}
		}
	}

	if tasksOK {
		for _, id := range sortedKeys(tasks) {
			task, ok := asMap(tasks[id])
			if !ok {
				continue
			}
			cadence, ok := asString(task["cadence"])
			dueObj, dueOK := asMap(task["due"])
			if !ok || !dueOK || !isCadence(cadence) {
				continue
			}
			if rule, ok := p.dueRule(id, cadence, pointer("tasks", id, "due"), dueObj); ok {
				p.rules[id] = rule
			}
		}
	}

	if cyclesOK {
		for _, id := range sortedKeys(cycles) {
			cy, ok := asMap(cycles[id])
			if !ok {
				continue
			}
			keys, _ := asList(cy["keys"])
			for i, k := range keys {
				if s, ok := asString(k); ok && s == "cycle_type" {
					p.add(pointer("cycles", id, "keys", strconv.Itoa(i)),
						`key "cycle_type" is reserved for the connector and cannot be a pre-filled key of cycle type %q`, id)
				}
			}
		}
	}

	if s, ok := asString(p.root["public_url"]); ok {
		if u, err := url.Parse(s); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			p.addUnlessReported("/public_url", "public_url %q must be an http or https URL such as https://focus.example.test", s)
		}
	}
}

func isCadence(s string) bool {
	for _, c := range cadences {
		if s == c {
			return true
		}
	}
	return false
}

// profile checks the ids one profile lists.
func (p *parser) profile(name string, prof, tasks map[string]any, tasksOK bool, cycles map[string]any, cyclesOK bool) {
	for _, cadence := range cadences {
		list, _ := asList(prof[cadence])
		seen := map[string]bool{}
		for i, e := range list {
			id, ok := asString(e)
			if !ok {
				continue
			}
			path := pointer("profiles", name, cadence, strconv.Itoa(i))
			if seen[id] {
				p.add(path, "profile %q lists task %q under %s more than once", name, id, cadence)
				continue
			}
			seen[id] = true
			if !tasksOK {
				continue
			}
			def, exists := tasks[id]
			if !exists {
				p.add(path, "profile %q lists task %q under %s, but tasks defines no task with that id", name, id, cadence)
				continue
			}
			if task, ok := asMap(def); ok {
				if own, ok := asString(task["cadence"]); ok && isCadence(own) && own != cadence {
					p.add(path, "profile %q lists task %q under %s, but its cadence is %s; list it under %s", name, id, cadence, own, own)
				}
			}
		}
	}

	list, _ := asList(prof["cycles"])
	seen := map[string]bool{}
	for i, e := range list {
		id, ok := asString(e)
		if !ok {
			continue
		}
		path := pointer("profiles", name, "cycles", strconv.Itoa(i))
		if seen[id] {
			p.add(path, "profile %q lists cycle type %q more than once", name, id)
			continue
		}
		seen[id] = true
		if _, exists := cycles[id]; cyclesOK && !exists {
			p.add(path, "profile %q lists cycle type %q, but cycles defines no cycle type with that id", name, id)
		}
	}
}

// dueRule checks that a task's due rule carries exactly the fields its cadence
// takes and that each is valid, and returns the rule when it is. A field that
// is missing, or not allowed, is reported at its own pointer.
func (p *parser) dueRule(id, cadence, path string, d map[string]any) (due.Rule, bool) {
	before := len(p.problems)
	_, hasWeekday := d["weekday"]
	_, hasDay := d["day"]
	switch cadence {
	case string(due.Daily):
		if hasWeekday {
			p.add(path+"/weekday", "task %q is daily, so its due rule takes only at and tz; remove weekday", id)
		}
		if hasDay {
			p.add(path+"/day", "task %q is daily, so its due rule takes only at and tz; remove day", id)
		}
	case string(due.Weekly):
		if !hasWeekday {
			p.add(path+"/weekday", "task %q is weekly, so its due rule requires weekday (mon to sun)", id)
		}
		if hasDay {
			p.add(path+"/day", "task %q is weekly, so its due rule takes at, tz and weekday; remove day", id)
		}
	case string(due.Sprint):
		if !hasDay {
			p.add(path+"/day", "task %q is sprint, so its due rule requires day (1 is the first date of the sprint)", id)
		}
		if hasWeekday {
			p.add(path+"/weekday", "task %q is sprint, so its due rule takes at, tz and day; remove weekday", id)
		}
	}

	var rule due.Rule
	ok := true
	if s, isString := asString(d["at"]); isString {
		at, err := civil.ParseTimeOfDay(s)
		if err != nil {
			p.addUnlessReported(path+"/at", "due time %q is not a 24-hour HH:MM time (two digits each, 00:00 to 23:59), such as 09:00", s)
			ok = false
		}
		rule.At = at
	} else {
		ok = false
	}
	if s, isString := asString(d["tz"]); isString {
		z, err := zone.Load(s)
		if err != nil {
			p.replace(path+"/tz", "%s", err.Error())
			ok = false
		}
		rule.TZ = z
	} else {
		ok = false
	}
	if w, present := d["weekday"]; present {
		if s, isString := asString(w); isString {
			wd, err := civil.ParseWeekday(s)
			if err != nil {
				p.addUnlessReported(path+"/weekday", "%s", err.Error())
				ok = false
			}
			rule.Weekday = &wd
		}
	}
	if v, present := d["day"]; present {
		switch n := v.(type) {
		case int:
			if n < 1 {
				p.addUnlessReported(path+"/day", "day %d must be a whole number of at least 1 (1 is the first date of the sprint)", n)
				ok = false
			}
			rule.Day = &n
		case json.Number:
			if wholeNumber.MatchString(string(n)) {
				p.addUnlessReported(path+"/day", "day %s is too large to be a day of a sprint", n)
			}
			ok = false
		}
	}

	if !ok || len(p.problems) > before {
		return due.Rule{}, false
	}
	if err := rule.Validate(due.Cadence(cadence)); err != nil {
		p.add(path, "task %q: %s", id, err.Error())
		return due.Rule{}, false
	}
	return rule, true
}

var wholeNumber = regexp.MustCompile(`^[0-9]+$`)

// ---- building the Config ----

// build assembles the Config of a document that has no problems. Every value it
// reads has passed the schema and the semantic rules.
func (p *parser) build() *Config {
	c := &Config{
		profiles:  map[string]Profile{},
		tasks:     map[string]TaskDef{},
		cycles:    map[string]CycleDef{},
		groupRank: map[string]int{},
	}
	defaults, _ := asMap(p.root["defaults"])
	c.defaults.CycleMinutes, _ = asInt(defaults["cycle_minutes"])
	c.defaults.Profile, _ = asString(defaults["profile"])

	c.defaults.BoostMinutes = defaultBoostMinutes()
	if list, ok := asList(defaults["boost_minutes"]); ok {
		c.defaults.BoostMinutes = ints(list)
	}
	c.defaults.MaxFutureSkewSeconds = defaultMaxFutureSkewSeconds
	if n, ok := asInt(defaults["max_future_skew_seconds"]); ok {
		c.defaults.MaxFutureSkewSeconds = n
	}
	c.defaults.Attention = Attention{
		DueSoonMinutes:      defaultDueSoonMinutes,
		OvertimeHighMinutes: defaultOvertimeHighMinutes,
		StalePauseMinutes:   defaultStalePauseMinutes,
	}
	if att, ok := asMap(defaults["attention"]); ok {
		if n, ok := asInt(att["due_soon_minutes"]); ok {
			c.defaults.Attention.DueSoonMinutes = n
		}
		if n, ok := asInt(att["overtime_high_minutes"]); ok {
			c.defaults.Attention.OvertimeHighMinutes = n
		}
		if n, ok := asInt(att["stale_pause_minutes"]); ok {
			c.defaults.Attention.StalePauseMinutes = n
		}
	}
	alert, _ := asMap(defaults["alert"])
	c.alert = alertOverride(alert)
	c.defaults.Alert = c.Alert("")

	c.listenPort, _ = asInt(p.root["listen_port"])
	c.publicURL, _ = asString(p.root["public_url"])

	order, _ := asList(p.root["group_order"])
	for i, g := range order {
		if s, ok := asString(g); ok {
			c.groupOrder = append(c.groupOrder, s)
			if _, dup := c.groupRank[s]; !dup {
				c.groupRank[s] = i
			}
		}
	}
	c.groups = len(order)

	profiles, _ := asMap(p.root["profiles"])
	for name, raw := range profiles {
		prof, _ := asMap(raw)
		c.profiles[name] = Profile{
			Name:   name,
			Daily:  stringList(prof["daily"]),
			Weekly: stringList(prof["weekly"]),
			Sprint: stringList(prof["sprint"]),
			Cycles: stringList(prof["cycles"]),
		}
	}

	tasks, _ := asMap(p.root["tasks"])
	for id, raw := range tasks {
		t, _ := asMap(raw)
		def := TaskDef{ID: id, Due: p.rules[id]}
		def.Title, _ = asString(t["title"])
		cadence, _ := asString(t["cadence"])
		def.Cadence = due.Cadence(cadence)
		def.Group, _ = asString(t["group"])
		def.Link, _ = asString(t["link"])
		c.tasks[id] = def
	}

	cycles, _ := asMap(p.root["cycles"])
	for id, raw := range cycles {
		cy, _ := asMap(raw)
		def := CycleDef{ID: id, Keys: stringList(cy["keys"])}
		def.Title, _ = asString(cy["title"])
		def.Minutes, _ = asInt(cy["minutes"])
		a, _ := asMap(cy["alert"])
		def.Alert = alertOverride(a)
		c.cycles[id] = def
	}
	return c
}

// soundOf reads one sound setting: nil when the key is absent, NoSound when it
// is null, the named sound otherwise. The schema has already checked the type.
func soundOf(m map[string]any, key string) *Sound {
	v, present := m[key]
	if !present {
		return nil
	}
	s := NoSound
	if name, ok := asString(v); ok {
		s = Named(name)
	}
	return &s
}

func alertOverride(m map[string]any) AlertOverride {
	var a AlertOverride
	a.Sound = soundOf(m, "sound")
	a.ReminderSound = soundOf(m, "reminder_sound")
	a.RepeatMinutes, _ = asInt(m["repeat_minutes"])
	return a
}

func ints(list []any) []int {
	out := make([]int, 0, len(list))
	for _, e := range list {
		if n, ok := asInt(e); ok {
			out = append(out, n)
		}
	}
	return out
}

func stringList(v any) []string {
	list, _ := asList(v)
	var out []string
	for _, e := range list {
		if s, ok := asString(e); ok {
			out = append(out, s)
		}
	}
	return out
}
