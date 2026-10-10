package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
)

const testdata = "../../testdata/config"

// validJSON is the bytes of the example configuration fixture.
func validJSON(t testing.TB) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdata, "valid.json"))
	if err != nil {
		t.Fatalf("reading the valid fixture: %v", err)
	}
	return raw
}

// mustParse parses raw and fails the test when it is rejected.
func mustParse(t testing.TB, raw []byte) *config.Config {
	t.Helper()
	c, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func mustParseValid(t testing.TB) *config.Config {
	t.Helper()
	return mustParse(t, validJSON(t))
}

// edited returns the valid fixture after f has edited its generic tree.
func edited(t testing.TB, f func(c map[string]any)) []byte {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal(validJSON(t), &c); err != nil {
		t.Fatal(err)
	}
	f(c)
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// at walks nested objects of a generic tree.
func at(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		m = m[p].(map[string]any)
	}
	return m
}

// validationError returns the *ValidationError of err, failing when err is
// anything else.
func validationError(t testing.TB, err error) *config.ValidationError {
	t.Helper()
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want *config.ValidationError", err, err)
	}
	return ve
}

// problemAt returns the first Problem at path.
func problemAt(ve *config.ValidationError, path string) (config.Problem, bool) {
	for _, p := range ve.Problems {
		if p.Path == path {
			return p, true
		}
	}
	return config.Problem{}, false
}

func paths(ve *config.ValidationError) []string {
	var out []string
	for _, p := range ve.Problems {
		out = append(out, p.Path)
	}
	return out
}

// TestParseValidExample pins the example configuration of the spec (as JSON,
// with example.test as the host) and the values it resolves to.
// INV-CONF-2, INV-CONF-5, INV-CONF-7: the layering, the duration precedence and
// the alert resolution.
func TestParseValidExample(t *testing.T) {
	c := mustParseValid(t)

	// INV-CONF-5: the start-time override, then the type's minutes, then
	// defaults.cycle_minutes.
	override := 15
	for _, tc := range []struct {
		name     string
		typ      string
		override *int
		want     int
	}{
		{"a type with its own minutes", "deep-work", nil, 50},
		{"another type", "review", nil, 25},
		{"the start-time override wins", "deep-work", &override, 15},
		{"the override wins for an unknown type", "no-such-type", &override, 15},
		{"an unknown type falls to the default", "no-such-type", nil, 25},
	} {
		if got := c.CycleMinutes(tc.typ, tc.override); got != tc.want {
			t.Errorf("%s: CycleMinutes(%q, %v) = %d, want %d", tc.name, tc.typ, tc.override, got, tc.want)
		}
	}

	// INV-CONF-7: per cycle type, then defaults.alert; a type that overrides
	// only the sound reminds with it, not with the defaults' reminder_sound.
	for typ, want := range map[string]config.Alert{
		"deep-work": {Sound: config.Named("Hero"), ReminderSound: config.Named("Hero"), RepeatMinutes: 10},
		"review":    {Sound: config.Named("Glass"), ReminderSound: config.Named("Tink"), RepeatMinutes: 5},
	} {
		if got := c.Alert(typ); got != want {
			t.Errorf("Alert(%q) = %+v, want %+v", typ, got, want)
		}
	}

	// INV-CONF-2: a profile lists ids; definitions live once.
	p, ok := c.Profile("normal")
	if !ok {
		t.Fatal("Profile(normal) not found")
	}
	wantProfile := config.Profile{
		Name:   "normal",
		Daily:  []string{"plan-day", "post-plan", "end-of-day-summary"},
		Weekly: []string{"weekly-update"},
		Sprint: []string{"capacity-check"},
		Cycles: []string{"notifications", "review", "deep-work"},
	}
	if !reflect.DeepEqual(p, wantProfile) {
		t.Errorf("Profile(normal) = %+v, want %+v", p, wantProfile)
	}
	if _, ok := c.Profile("on-call"); !ok {
		t.Error("Profile(on-call) not found")
	}
	if _, ok := c.Profile("absent"); ok {
		t.Error("Profile(absent) found")
	}
	if got := c.Defaults().Profile; got != "normal" {
		t.Errorf("Defaults().Profile = %q, want normal", got)
	}
}

// TestParseWithNoReminderSoundAnywhere pins INV-CONF-7: reminder_sound
// defaults to sound, both in the defaults and in a cycle type that overrides
// the sound.
func TestParseWithNoReminderSoundAnywhere(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) {
		delete(at(c, "defaults", "alert"), "reminder_sound")
	}))
	for typ, want := range map[string]config.Alert{
		"review":    {Sound: config.Named("Glass"), ReminderSound: config.Named("Glass"), RepeatMinutes: 5},
		"deep-work": {Sound: config.Named("Hero"), ReminderSound: config.Named("Hero"), RepeatMinutes: 10},
	} {
		if got := c.Alert(typ); got != want {
			t.Errorf("Alert(%q) = %+v, want %+v", typ, got, want)
		}
	}
	if got := c.Defaults().Alert; got != (config.Alert{Sound: config.Named("Glass"), ReminderSound: config.Named("Glass"), RepeatMinutes: 5}) {
		t.Errorf("Defaults().Alert = %+v, want the reminder to equal the sound", got)
	}
}

// TestParseNoSound pins INV-CONF-7: null is the explicit "no sound", a setting
// distinct from an absent key, in the defaults and in a cycle type. A document
// that spells null differs from one that leaves the key out.
func TestParseNoSound(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) {
		at(c, "defaults", "alert")["reminder_sound"] = nil
		at(c, "cycles", "deep-work", "alert")["sound"] = nil
	}))
	for typ, want := range map[string]config.Alert{
		"review":        {Sound: config.Named("Glass"), ReminderSound: config.NoSound, RepeatMinutes: 5},
		"deep-work":     {Sound: config.NoSound, ReminderSound: config.NoSound, RepeatMinutes: 10},
		"not-a-type-id": {Sound: config.Named("Glass"), ReminderSound: config.NoSound, RepeatMinutes: 5},
	} {
		if got := c.Alert(typ); got != want {
			t.Errorf("Alert(%q) = %+v, want %+v", typ, got, want)
		}
	}
	if got, want := c.Defaults().Alert, (config.Alert{Sound: config.Named("Glass"), ReminderSound: config.NoSound, RepeatMinutes: 5}); got != want {
		t.Errorf("Defaults().Alert = %+v, want %+v", got, want)
	}
	cy, ok := c.CycleType("deep-work")
	if !ok || cy.Alert.Sound == nil || !cy.Alert.Sound.None() || cy.Alert.ReminderSound != nil {
		t.Errorf("CycleType(deep-work).Alert = %+v, want an explicit no sound and an absent reminder_sound", cy.Alert)
	}

	// defaults.alert.sound can be null too (the key stays required); the
	// default reminder_sound of the fixture still plays.
	d := mustParse(t, edited(t, func(c map[string]any) { at(c, "defaults", "alert")["sound"] = nil }))
	if got, want := d.Alert("review"), (config.Alert{Sound: config.NoSound, ReminderSound: config.Named("Tink"), RepeatMinutes: 5}); got != want {
		t.Errorf("Alert(review) with defaults.alert.sound null = %+v, want %+v", got, want)
	}
}

// TestSoundEncodesAsNameOrNull pins that a client reading the state can tell
// no sound (null) from a name.
func TestSoundEncodesAsNameOrNull(t *testing.T) {
	out, err := json.Marshal(config.Alert{Sound: config.Named("Glass"), ReminderSound: config.NoSound, RepeatMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"Sound":"Glass","ReminderSound":null,"RepeatMinutes":5}`; string(out) != want {
		t.Errorf("Marshal = %s, want %s", out, want)
	}
	if config.Named("").None() != true || config.Named("x").None() || config.Named("x").Name() != "x" || config.NoSound.Name() != "" {
		t.Error("Named/None/Name disagree")
	}
}

// TestSoundIsRequiredAndNeverEmpty pins that no sound is spelled null, never
// an empty string or a missing key in defaults.alert.
func TestSoundIsRequiredAndNeverEmpty(t *testing.T) {
	for name, edit := range map[string]func(c map[string]any){
		"an empty default sound":      func(c map[string]any) { at(c, "defaults", "alert")["sound"] = "" },
		"an empty cycle sound":        func(c map[string]any) { at(c, "cycles", "deep-work", "alert")["sound"] = "" },
		"an empty cycle reminder":     func(c map[string]any) { at(c, "cycles", "deep-work", "alert")["reminder_sound"] = "" },
		"a numeric reminder":          func(c map[string]any) { at(c, "defaults", "alert")["reminder_sound"] = 3 },
		"a boolean cycle sound":       func(c map[string]any) { at(c, "cycles", "deep-work", "alert")["sound"] = false },
		"a list as the default sound": func(c map[string]any) { at(c, "defaults", "alert")["sound"] = []any{"A"} },
	} {
		if _, err := config.Parse(edited(t, edit)); err == nil {
			t.Errorf("%s: Parse accepted it", name)
		}
	}
}

func soundPtr(name string) *config.Sound {
	s := config.Named(name)
	return &s
}

// TestTaskAndCycleTypeAccessors pins the definitions a Config hands out.
func TestTaskAndCycleTypeAccessors(t *testing.T) {
	c := mustParseValid(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	task, ok := c.Task("weekly-update")
	if !ok {
		t.Fatal("Task(weekly-update) not found")
	}
	if task.ID != "weekly-update" || task.Title != "Write the weekly status update" || task.Cadence != due.Weekly || task.Group != "" || task.Link != "" {
		t.Errorf("Task(weekly-update) = %+v", task)
	}
	if task.Due.At != (civil.TimeOfDay{Hour: 9}) || task.Due.TZ.Name() != "America/New_York" || task.Due.Weekday == nil || *task.Due.Weekday != time.Thursday || task.Due.Day != nil {
		t.Errorf("Task(weekly-update).Due = %+v", task.Due)
	}
	if got, ok := c.Task("plan-day"); !ok || got.Group != "Start of day" || got.Cadence != due.Daily {
		t.Errorf("Task(plan-day) = %+v, %v", got, ok)
	}
	if _, ok := c.Task("absent"); ok {
		t.Error("Task(absent) found")
	}

	cy, ok := c.CycleType("deep-work")
	if !ok {
		t.Fatal("CycleType(deep-work) not found")
	}
	wantCycle := config.CycleDef{
		ID: "deep-work", Title: "Deep work cycle", Minutes: 50, Keys: []string{"ticket", "pr"},
		Alert: config.AlertOverride{Sound: soundPtr("Hero"), RepeatMinutes: 10},
	}
	if !reflect.DeepEqual(cy, wantCycle) {
		t.Errorf("CycleType(deep-work) = %+v, want %+v", cy, wantCycle)
	}
	if _, ok := c.CycleType("absent"); ok {
		t.Error("CycleType(absent) found")
	}

	// INV-CONF-4: the parsed rule is the one due.Resolve consumes, resolving in
	// the rule's own zone: Thursday of the week of 2026-10-05, 09:00 in New York.
	res, err := due.Resolve(due.Weekly, task.Due, civil.Date{Year: 2026, Month: 10, Day: 5}, civil.Date{Year: 2026, Month: 10, Day: 11})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := time.Date(2026, 10, 8, 9, 0, 0, 0, ny).UTC(); !res.Instant.Equal(want) {
		t.Errorf("weekly rule resolved to %v, want %v", res.Instant, want)
	}
}

// TestAccessorsHandOutCopies pins that a Config is immutable: nothing a caller
// does to a returned value reaches it.
func TestAccessorsHandOutCopies(t *testing.T) {
	c := mustParseValid(t)
	before := c.Digest()

	p, _ := c.Profile("normal")
	p.Daily[0] = "changed"
	if again, _ := c.Profile("normal"); again.Daily[0] != "plan-day" {
		t.Error("changing a returned profile changed the configuration")
	}
	d := c.Defaults()
	d.BoostMinutes[0] = 999
	if c.Defaults().BoostMinutes[0] != 5 {
		t.Error("changing the returned defaults changed the configuration")
	}
	cy, _ := c.CycleType("deep-work")
	cy.Keys[0] = "changed"
	if again, _ := c.CycleType("deep-work"); again.Keys[0] != "ticket" {
		t.Error("changing a returned cycle type changed the configuration")
	}
	task, _ := c.Task("weekly-update")
	*task.Due.Weekday = time.Monday
	if again, _ := c.Task("weekly-update"); *again.Due.Weekday != time.Thursday {
		t.Error("changing a returned due rule changed the configuration")
	}
	if c.Digest() != before {
		t.Error("the digest changed")
	}
}

// TestDefaultsOfOptionalFields pins the values the optional `defaults` fields
// take when the configuration leaves them out, and that a configuration can
// set them. INV-CONF-18: max_future_skew_seconds defaults to 60.
func TestDefaultsOfOptionalFields(t *testing.T) {
	minimal := []byte(`{
	  "defaults": {"cycle_minutes": 25, "profile": "p", "alert": {"sound": "Glass", "repeat_minutes": 5}},
	  "listen_port": 1,
	  "profiles": {"p": {}},
	  "tasks": {},
	  "cycles": {}
	}`)
	got := mustParse(t, minimal).Defaults()
	want := config.Defaults{
		CycleMinutes:         25,
		BoostMinutes:         []int{5, 10, 25},
		Profile:              "p",
		MaxFutureSkewSeconds: 60,
		Alert:                config.Alert{Sound: config.Named("Glass"), ReminderSound: config.Named("Glass"), RepeatMinutes: 5},
		Attention:            config.Attention{DueSoonMinutes: 30, OvertimeHighMinutes: 15, StalePauseMinutes: 45},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Defaults() = %+v, want %+v", got, want)
	}

	// The valid fixture sets all of them, so they pass through.
	set := mustParseValid(t).Defaults()
	if set.MaxFutureSkewSeconds != 60 || !reflect.DeepEqual(set.BoostMinutes, []int{5, 10, 25}) || set.Attention != want.Attention {
		t.Errorf("Defaults() of the fixture = %+v", set)
	}

	// INV-CONF-18: an operator value replaces the default, 0 included; a partly
	// given attention section keeps the defaults of the other thresholds.
	custom := mustParse(t, edited(t, func(c map[string]any) {
		d := at(c, "defaults")
		d["max_future_skew_seconds"] = 0
		d["boost_minutes"] = []any{3}
		d["attention"] = map[string]any{"due_soon_minutes": 7}
	})).Defaults()
	if custom.MaxFutureSkewSeconds != 0 || !reflect.DeepEqual(custom.BoostMinutes, []int{3}) {
		t.Errorf("custom Defaults() = %+v", custom)
	}
	if custom.Attention != (config.Attention{DueSoonMinutes: 7, OvertimeHighMinutes: 15, StalePauseMinutes: 45}) {
		t.Errorf("custom attention = %+v", custom.Attention)
	}
	// An empty boost list is a choice too: no boost buttons.
	none := mustParse(t, edited(t, func(c map[string]any) { at(c, "defaults")["boost_minutes"] = []any{} })).Defaults()
	if len(none.BoostMinutes) != 0 {
		t.Errorf("an empty boost_minutes became %v", none.BoostMinutes)
	}
}

// TestListenPortAndPublicURL pins INV-CONF-13: listen_port is required and
// public_url is optional and an http or https URL.
func TestListenPortAndPublicURL(t *testing.T) {
	c := mustParseValid(t)
	if c.ListenPort() != 49210 {
		t.Errorf("ListenPort() = %d, want 49210", c.ListenPort())
	}
	if c.PublicURL() != "https://focus.example.test" {
		t.Errorf("PublicURL() = %q", c.PublicURL())
	}
	none := mustParse(t, edited(t, func(c map[string]any) { delete(c, "public_url") }))
	if none.PublicURL() != "" {
		t.Errorf("PublicURL() without one = %q, want empty", none.PublicURL())
	}
	for _, ok := range []string{"http://focus.example.test", "https://focus.example.test:8443/base", "http://127.0.0.1:49210", "https://[::1]:8080"} {
		mustParse(t, edited(t, func(c map[string]any) { c["public_url"] = ok }))
	}
}

// TestZoneNamesFollowTheStandardLibrary pins D12 as the loader applies it:
// any name the standard library resolves is accepted, including the legacy
// POSIX-style names.
func TestZoneNamesFollowTheStandardLibrary(t *testing.T) {
	for _, tz := range []string{"EST5EDT", "UTC", "Europe/Paris", "Asia/Tokyo", "Australia/Lord_Howe"} {
		c := mustParse(t, edited(t, func(c map[string]any) { at(c, "tasks", "plan-day", "due")["tz"] = tz }))
		if task, _ := c.Task("plan-day"); task.Due.TZ.Name() != tz {
			t.Errorf("tz %q parsed as %q", tz, task.Due.TZ.Name())
		}
	}
}

// TestCycleKeysAcceptTheKeyAlphabet pins INV-CONF-6 for what is accepted: key
// names of lower-case letters, digits, underscore and hyphen. (What is
// rejected is in TestRule7Rejections.)
func TestCycleKeysAcceptTheKeyAlphabet(t *testing.T) {
	c := mustParse(t, edited(t, func(c map[string]any) {
		at(c, "cycles", "review")["keys"] = []any{"pr", "a-b_9", "0", "cycle-type", "cycle_types"}
	}))
	cy, _ := c.CycleType("review")
	if want := []string{"pr", "a-b_9", "0", "cycle-type", "cycle_types"}; !reflect.DeepEqual(cy.Keys, want) {
		t.Errorf("Keys = %v, want %v", cy.Keys, want)
	}
}

// TestParseNormalizesNumbers pins that a number spelled 25.0 or 2.5e1 is the
// integer 25: the schema accepts it as an integer, and so does the loader.
func TestParseNormalizesNumbers(t *testing.T) {
	plain := mustParseValid(t)
	spelled := strings.NewReplacer(
		`"cycle_minutes": 25`, `"cycle_minutes": 25.0`,
		`"listen_port": 49210`, `"listen_port": 4.921e4`,
		`"day": 1`, `"day": 1e0`,
	).Replace(string(validJSON(t)))
	if spelled == string(validJSON(t)) {
		t.Fatal("the fixture has none of the numbers to respell")
	}
	c := mustParse(t, []byte(spelled))
	if c.ListenPort() != 49210 || c.Defaults().CycleMinutes != 25 {
		t.Errorf("ListenPort() = %d, CycleMinutes = %d", c.ListenPort(), c.Defaults().CycleMinutes)
	}
	if c.Digest() != plain.Digest() {
		t.Errorf("the digest depends on the spelling of a number: %s vs %s", c.Digest(), plain.Digest())
	}
}

// TestDigestStable pins that the digest ignores key order and whitespace and
// changes with any value.
func TestDigestStable(t *testing.T) {
	raw := validJSON(t)
	base := mustParse(t, raw).Digest()
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(base) {
		t.Fatalf("Digest() = %q, want 64 lower-case hex digits", base)
	}
	if again := mustParse(t, raw).Digest(); again != base {
		t.Errorf("the digest of one document differs between parses: %s vs %s", again, base)
	}

	// Key order and whitespace: Go marshals a map with sorted keys and no
	// whitespace; indenting with tabs changes the whitespace again; reversing
	// the order of the keys of one object changes the order.
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	compact, _ := json.Marshal(tree)
	var tabbed bytes.Buffer
	if err := json.Indent(&tabbed, compact, "", "\t\t"); err != nil {
		t.Fatal(err)
	}
	reordered := reorderedConfig(t)
	for name, doc := range map[string][]byte{
		"compact":               compact,
		"tab-indented":          tabbed.Bytes(),
		"trailing whitespace":   append(append([]byte("  \n"), raw...), "\n\n\t "...),
		"keys in another order": reordered,
	} {
		if got := mustParse(t, doc).Digest(); got != base {
			t.Errorf("%s: digest %s differs from %s", name, got, base)
		}
	}

	// Any value change changes the digest.
	for name, edit := range map[string]func(c map[string]any){
		"listen_port":           func(c map[string]any) { c["listen_port"] = 49211 },
		"public_url":            func(c map[string]any) { c["public_url"] = "https://other.example.test" },
		"public_url removed":    func(c map[string]any) { delete(c, "public_url") },
		"default sound":         func(c map[string]any) { at(c, "defaults", "alert")["sound"] = "Ping" },
		"default sound to none": func(c map[string]any) { at(c, "defaults", "alert")["sound"] = nil },
		"a cycle reminder none": func(c map[string]any) { at(c, "cycles", "deep-work", "alert")["reminder_sound"] = nil },
		"a task title":          func(c map[string]any) { at(c, "tasks", "plan-day")["title"] = "Plan the day well" },
		"a due time":            func(c map[string]any) { at(c, "tasks", "plan-day", "due")["at"] = "09:01" },
		"a due zone":            func(c map[string]any) { at(c, "tasks", "plan-day", "due")["tz"] = "UTC" },
		"a group order":         func(c map[string]any) { c["group_order"] = []any{"End of day", "During the day", "Start of day"} },
		"a boost":               func(c map[string]any) { at(c, "defaults")["boost_minutes"] = []any{5, 10, 26} },
		"an added profile":      func(c map[string]any) { at(c, "profiles")["extra"] = map[string]any{} },
		"a key of a cycle":      func(c map[string]any) { at(c, "cycles", "review")["keys"] = []any{"pr", "extra"} },
		"a default spelled out": func(c map[string]any) { at(c, "defaults")["attention"] = map[string]any{"due_soon_minutes": 30} },
		"a skew":                func(c map[string]any) { at(c, "defaults")["max_future_skew_seconds"] = 61 },
	} {
		if got := mustParse(t, edited(t, edit)).Digest(); got == base {
			t.Errorf("changing %s did not change the digest", name)
		}
	}
}

// reorderedConfig returns the fixture with the keys of every object in
// reverse order.
func reorderedConfig(t testing.TB) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(validJSON(t)))
	dec.UseNumber()
	var out bytes.Buffer
	var rewrite func() any
	type member struct {
		key string
		val any
	}
	type object []member
	rewrite = func() any {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{':
				var o object
				for dec.More() {
					k, _ := dec.Token()
					o = append(o, member{k.(string), rewrite()})
				}
				_, _ = dec.Token()
				return o
			case '[':
				var a []any
				for dec.More() {
					a = append(a, rewrite())
				}
				_, _ = dec.Token()
				return a
			}
		}
		return tok
	}
	var emit func(v any)
	emit = func(v any) {
		switch x := v.(type) {
		case object:
			out.WriteByte('{')
			for i := len(x) - 1; i >= 0; i-- {
				k, _ := json.Marshal(x[i].key)
				out.Write(k)
				out.WriteByte(':')
				emit(x[i].val)
				if i > 0 {
					out.WriteByte(',')
				}
			}
			out.WriteByte('}')
		case []any:
			out.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					out.WriteByte(',')
				}
				emit(e)
			}
			out.WriteByte(']')
		default:
			b, _ := json.Marshal(x)
			out.Write(b)
		}
	}
	emit(rewrite())
	return out.Bytes()
}

// TestFixturesAreGeneric pins INV-CONF-1: the fixtures and examples carry only
// generic text and the example.test host.
func TestFixturesAreGeneric(t *testing.T) {
	seen := 0
	err := filepath.WalkDir(testdata, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen++
		for _, m := range regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^"\s]*`).FindAllString(string(raw), -1) {
			u, err := url.Parse(m)
			if err != nil {
				t.Errorf("%s: unparsable URL %q: %v", path, m, err)
				continue
			}
			host := u.Hostname()
			// ftp://x and https://:8080 are deliberately invalid URLs; they name no real host.
			if host == "" || host == "x" || host == "example.test" || strings.HasSuffix(host, ".example.test") || host == "127.0.0.1" || host == "::1" {
				continue
			}
			t.Errorf("%s: URL %q does not use the example.test host", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 20 {
		t.Errorf("walked only %d fixtures", seen)
	}
}
